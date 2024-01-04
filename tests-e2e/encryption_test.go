package syncv3_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestEncryptionFallbackKey(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.MustCreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)

	// snaffled from rust SDK
	keysUploadBody := fmt.Sprintf(`{
		"device_keys": {
			"algorithms": [
				"m.olm.v1.curve25519-aes-sha2",
				"m.megolm.v1.aes-sha2"
			],
			"device_id": "MUPCQIATEC",
			"keys": {
				"curve25519:MUPCQIATEC": "NroPrV4HHJ/Wj0A0XMrHt7IuThVnwpT6tRZXQXkO4kI",
				"ed25519:MUPCQIATEC": "G9zNR/pZb24Rm0FXiQYutSzcbQvii+AZn/4cmi6LOUI"
			},
			"signatures": {
				"%s": {
					"ed25519:MUPCQIATEC": "2CHK2tJO/p2OiNWC2jLKsH5t+pHwnomSHOIpAPuEVi2vJZ4BRRsb4tSFYzEx4cUDg3KCYjoQuCymYHpnk1uqDQ"
				}
			},
			"user_id": "%s"
		},
		"fallback_keys": {
			"signed_curve25519:AAAAAAAAAAA": {
				"fallback": true,
				"key": "s5+eOJYK1s5xPt51BlYEXx8fQ8NqpwAUjE1mVxw05V8",
				"signatures": {
					"%s": {
						"ed25519:MUPCQIATEC": "TLGi0LJEDxgt37gBCpd8huZa72h0UTB8jIEUoTz/rjbCcGQo1xOlvA5rU+RoTkF1KwVtduOMbZcSGg4ZTfBkDQ"
					}
				}
			}
		},
		"one_time_keys": {
			"signed_curve25519:AAAAAAAAAA0": {
				"key": "IuCQvr2AaZC70tCG6g1ZardACNe3mcKZ2PjKJ2p49UM",
				"signatures": {
					"%s": {
						"ed25519:MUPCQIATEC": "FXBkzwuLkfriWJ1B2z9wTHvi7WTOZGvs2oSNJ7CycXJYC6k06sa7a+OMQtpMP2RTuIpiYC+wZ3nFoKp1FcCcBQ"
					}
				}
			},
			"signed_curve25519:AAAAAAAAAA4": {
				"key": "pgeLFCJPLYUtyLPKDPr76xRYgPjjY4/lEUH98tExxCo",
				"signatures": {
					"%s": {
						"ed25519:MUPCQIATEC": "/o44D5qjTdiYORSXmCVYE3Vzvbz2OlIBC58ELe+EAAgIZTJyDxmBJIFotP6CIuFmB/p4lGCd41Fb6T5BnmLvBQ"
					}
				}
			},
			"signed_curve25519:AAAAAAAAAA8": {
				"key": "gAhoEOtrGTEG+gfAsCU+JS7+wJTlC51+kZ9vLr9BZGA",
				"signatures": {
					"%s": {
						"ed25519:MUPCQIATEC": "DLDj1c2UncqcCrEwSUEf31ni6W+E6D58EEGFIWj++ydBxuiEnHqFMF7AZU8GGcjQBDIH13uNe8xxO7/KeBbUDQ"
					}
				}
			}
		}
	}`, bob.UserID, bob.UserID, bob.UserID, bob.UserID, bob.UserID, bob.UserID)

	bob.MustDo(t, "POST", []string{"_matrix", "client", "v3", "keys", "upload"},
		client.WithRawBody([]byte(keysUploadBody)), client.WithContentType("application/json"),
	)

	res := bob.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			E2EE: &extensions.E2EERequest{
				Core: extensions.Core{
					Enabled: &boolTrue,
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchFallbackKeyTypes([]string{"signed_curve25519"}), m.MatchOTKCounts(map[string]int{
		"signed_curve25519": 3,
	}))

	// claim a OTK, it should decrease the count
	mustClaimOTK(t, alice, bob)
	// claiming OTKs does not wake up the sync loop, so send something to kick it.
	alice.MustSendTyping(t, roomID, true, 1000)
	res = bob.SlidingSyncUntil(t, res.Pos, sync3.Request{},
		// OTK was claimed so change should be included.
		// fallback key was not touched so should be missing.
		MatchOTKAndFallbackTypes(map[string]int{
			"signed_curve25519": 2,
		}, nil),
	)

	mustClaimOTK(t, alice, bob)
	alice.MustSendTyping(t, roomID, false, 1000)
	res = bob.SlidingSyncUntil(t, res.Pos, sync3.Request{},
		// OTK was claimed so change should be included.
		// fallback key was not touched so should be missing.
		MatchOTKAndFallbackTypes(map[string]int{
			"signed_curve25519": 1,
		}, nil),
	)

	mustClaimOTK(t, alice, bob)
	alice.MustSendTyping(t, roomID, true, 1000)
	res = bob.SlidingSyncUntil(t, res.Pos, sync3.Request{},
		// OTK was claimed so change should be included.
		// fallback key was not touched so should be missing.
		MatchOTKAndFallbackTypes(map[string]int{
			"signed_curve25519": 0,
		}, nil),
	)

	mustClaimOTK(t, alice, bob)
	alice.MustSendTyping(t, roomID, false, 1000)
	res = bob.SlidingSyncUntil(t, res.Pos, sync3.Request{},
		// no OTK change here so it shouldn't be included.
		// we should be explicitly sent device_unused_fallback_key_types: []
		MatchOTKAndFallbackTypes(nil, []string{}),
	)

	// now re-upload a fallback key, it should be repopulated.
	keysUploadBody = fmt.Sprintf(`{
		"fallback_keys": {
			"signed_curve25519:AAAAAAAAADA": {
				"fallback": true,
				"key": "N8DKj83RTN7lLZrH6shMqHbVhNrxd96OQseQVFmNgTU",
				"signatures": {
					"%s": {
						"ed25519:MUPCQIATEC": "ZnKsVcNmOLBv0LMGeNpCfCO2am9L223EiyddWPx9wPOtuYt6KZIPox/SFwVmqBwkUdnmeTb6tVgCpZwcH8doDw"
					}
				}
			}
		}
	}`, bob.UserID)
	bob.MustDo(t, "POST", []string{"_matrix", "client", "v3", "keys", "upload"},
		client.WithRawBody([]byte(keysUploadBody)), client.WithContentType("application/json"),
	)

	alice.MustSendTyping(t, roomID, true, 1000)
	res = bob.SlidingSyncUntil(t, res.Pos, sync3.Request{},
		// no OTK change here so it shouldn't be included.
		// we should be explicitly sent device_unused_fallback_key_types: ["signed_curve25519"]
		MatchOTKAndFallbackTypes(nil, []string{"signed_curve25519"}),
	)

	// another claim should remove it
	mustClaimOTK(t, alice, bob)

	alice.MustSendTyping(t, roomID, false, 1000)
	res = bob.SlidingSyncUntil(t, res.Pos, sync3.Request{},
		// no OTK change here so it shouldn't be included.
		// we should be explicitly sent device_unused_fallback_key_types: []
		MatchOTKAndFallbackTypes(nil, []string{}),
	)
}

func MatchOTKAndFallbackTypes(otkCount map[string]int, fallbackKeyTypes []string) m.RespMatcher {
	return func(r *sync3.Response) error {
		err := m.MatchOTKCounts(otkCount)(r)
		if err != nil {
			return err
		}
		// we should explicitly be sent device_unused_fallback_key_types: []
		return m.MatchFallbackKeyTypes(fallbackKeyTypes)(r)
	}
}

func mustClaimOTK(t *testing.T, claimer, claimee *CSAPI) {
	claimRes := claimer.MustDo(t, "POST", []string{"_matrix", "client", "v3", "keys", "claim"}, client.WithJSONBody(t, map[string]any{
		"one_time_keys": map[string]any{
			claimee.UserID: map[string]any{
				claimee.DeviceID: "signed_curve25519",
			},
		},
	}))
	var res struct {
		Failures map[string]any            `json:"failures"`
		OTKs     map[string]map[string]any `json:"one_time_keys"`
	}
	if err := json.NewDecoder(claimRes.Body).Decode(&res); err != nil {
		t.Fatalf("failed to decode OTK response: %s", err)
	}
	if len(res.Failures) > 0 {
		t.Fatalf("OTK response had failures: %+v", res.Failures)
	}
	otk := res.OTKs[claimee.UserID][claimee.DeviceID]
	if otk == nil {
		t.Fatalf("OTK was not claimed for %s|%s", claimee.UserID, claimee.DeviceID)
	}
}
