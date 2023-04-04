package syncv3_test

import (
	"encoding/json"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"testing"
)

func TestAccountDataRespectsExtensionScope(t *testing.T) {
	alice := registerNewUser(t)

	var syncResp *sync3.Response

	// Want at least one test of the initial sync behaviour (which hits `ProcessInitial`)
	// separate to the incremental sync behaviour (hits `AppendLive`)
	t.Log("Alice creates rooms 1 and 2.")
	room1 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 1"})
	room2 := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room 2"})
	t.Logf("room1=%s room2=%s", room1, room2)

	t.Log("Alice uploads account data for both rooms, plus global account data.")
	globalAccountDataEvent := putGlobalAccountData(
		t,
		alice,
		"com.example.global",
		map[string]interface{}{"global": "GLOBAL!", "version": 1},
	)
	putRoomAccountData(
		t,
		alice,
		room1,
		"com.example.room",
		map[string]interface{}{"room": 1, "version": 1},
	)
	putRoomAccountData(
		t,
		alice,
		room2,
		"com.example.room",
		map[string]interface{}{"room": 2, "version": 2},
	)

	t.Log("Alice makes an initial sync request, requesting global account data only.")
	syncResp = alice.SlidingSync(t, sync3.Request{
		Extensions: extensions.Request{
			AccountData: &extensions.AccountDataRequest{
				Core: extensions.Core{Enabled: &boolTrue, Lists: []string{}, Rooms: []string{}},
			},
		},
		Lists: map[string]sync3.RequestList{
			"window": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})

	t.Log("Alice should see her global account data only.")
	m.MatchResponse(
		t,
		syncResp,
		m.MatchHasGlobalAccountData(globalAccountDataEvent),
		m.MatchNoRoomAccountData([]string{room1, room2}),
	)

	t.Log("Alice updates her global account data.")
	globalAccountDataEvent = putGlobalAccountData(
		t,
		alice,
		"com.example.global",
		map[string]interface{}{"global": "GLOBAL!", "version": 2},
	)

	t.Log("Alice syncs until she sees the new data, requesting—but not expecting—account data for room 2.")
	syncResp = alice.SlidingSyncUntil(
		t,
		syncResp.Pos,
		sync3.Request{
			Extensions: extensions.Request{
				AccountData: &extensions.AccountDataRequest{
					Core: extensions.Core{Enabled: &boolTrue, Lists: []string{}, Rooms: []string{room2}},
				},
			},
		},
		func(response *sync3.Response) error {
			err := m.MatchNoRoomAccountData([]string{room1, room2})(response)
			if err != nil {
				t.Error(err)
			}
			return m.MatchHasGlobalAccountData(globalAccountDataEvent)(response)
		},
	)

	t.Log("Alice updates account data in both rooms")
	putRoomAccountData(
		t,
		alice,
		room1,
		"com.example.room",
		map[string]interface{}{"room": 1, "version": 1},
	)
	room2AccountDataEvent := putRoomAccountData(
		t,
		alice,
		room2,
		"com.example.room",
		map[string]interface{}{"room": 2, "version": 2},
	)

	t.Log("Alice syncs until she sees the account data for room 2. She shouldn't see account data for room 1")
	syncResp = alice.SlidingSyncUntil(
		t,
		syncResp.Pos,
		sync3.Request{}, // as before
		func(response *sync3.Response) error {
			err := m.MatchNoRoomAccountData([]string{room1})(response)
			if err != nil {
				t.Error(err)
			}
			return m.MatchAccountData(nil, map[string][]json.RawMessage{room2: {room2AccountDataEvent}})(response)
		},
	)

}

// putAccountData is a wrapper around SetGlobalAccountData. It returns the account data
// event as a json.RawMessage, automatically including top-level `type` and `content`
// fields. This is useful because it can be compared with account data events in a
// sync response with bytes.Equal.
func putGlobalAccountData(t *testing.T, client *CSAPI, eventType string, content map[string]interface{}) json.RawMessage {
	t.Helper()
	client.SetGlobalAccountData(t, eventType, content)
	serialised := testutils.NewAccountData(t, eventType, content)
	return serialised
}

// putRoomAccountData is like putGlobalAccountData, but for room-specific account data.
func putRoomAccountData(t *testing.T, client *CSAPI, roomID, eventType string, content map[string]interface{}) json.RawMessage {
	t.Helper()
	client.SetRoomAccountData(t, roomID, eventType, content)
	serialised := testutils.NewAccountData(t, eventType, content)
	return serialised
}
