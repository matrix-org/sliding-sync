package syncv3

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

type mockV2Client struct {
	requester string
	ch        chan *sync2.SyncResponse
}

func (c *mockV2Client) DoSyncV2(authHeader, since string) (*sync2.SyncResponse, int, error) {
	resp := <-c.ch
	return resp, 200, nil
}
func (c *mockV2Client) WhoAmI(authHeader string) (string, error) {
	return c.requester, nil
}

func marshalJSON(t *testing.T, in map[string]interface{}) json.RawMessage {
	t.Helper()
	j, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshalJSON: %s", err)
	}
	return j
}

func parseResponse(t *testing.T, body *bytes.Buffer) *sync3.Response {
	t.Helper()
	var v3Resp sync3.Response
	if err := json.Unmarshal(body.Bytes(), &v3Resp); err != nil {
		t.Fatalf("failed to unmarshal response: %s", err)
	}
	return &v3Resp
}

func TestHandler(t *testing.T) {
	alice := "@alice:localhost"
	aliceBearer := "Bearer alice_access_token"
	bob := "@bob:localhost"
	roomID := "!foo:localhost"
	v2ServerChan := make(chan *sync2.SyncResponse, 10)
	// disable colours in tests to make it display nicer in IDEs
	log := zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		NoColor:    true,
	})
	wrapper := hlog.NewHandler(log)
	h := NewSyncV3Handler(&mockV2Client{
		requester: alice,
		ch:        v2ServerChan,
	}, postgresConnectionString)
	server := wrapper(h)

	// prepare a response from v2
	v2Resp := &sync2.SyncResponse{
		NextBatch: "don't care",
	}
	v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
	v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
		State: struct {
			Events []json.RawMessage `json:"events"`
		}{
			Events: []json.RawMessage{
				marshalJSON(t, map[string]interface{}{
					"event_id": "$1", "sender": bob, "type": "m.room.create", "state_key": "", "content": map[string]interface{}{
						"creator": bob,
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$2", "sender": bob, "type": "m.room.join_rules", "state_key": "", "content": map[string]interface{}{
						"join_rule": "public",
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$3", "sender": bob, "type": "m.room.member", "state_key": bob, "content": map[string]interface{}{
						"membership": "join",
					}}),
				marshalJSON(t, map[string]interface{}{
					"event_id": "$4", "sender": alice, "type": "m.room.member", "state_key": alice, "content": map[string]interface{}{
						"membership": "join",
					}}),
			},
		},
	}
	v2ServerChan <- v2Resp

	// fresh user should make a new session and start polling, getting these events above.
	// however, we didn't ask for them so they shouldn't be returned.
	w := httptest.NewRecorder()
	w.Body = bytes.NewBuffer(nil)
	req := httptest.NewRequest("POST", "/_matrix/client/v3/sync", bytes.NewBuffer(marshalJSON(t, map[string]interface{}{
		"typing": map[string]interface{}{
			"room_id": roomID,
		},
	})))
	req.Header.Set("Authorization", aliceBearer)
	server.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("/v3/sync returned HTTP %d want 200", w.Code)
	}
	resp := parseResponse(t, w.Body)

	// now set bob to typing
	v2Resp = &sync2.SyncResponse{
		NextBatch: "still don't care",
	}
	v2Resp.Rooms.Join = make(map[string]sync2.SyncV2JoinResponse)
	v2Resp.Rooms.Join[roomID] = sync2.SyncV2JoinResponse{
		Ephemeral: struct {
			Events []json.RawMessage `json:"events"`
		}{
			Events: []json.RawMessage{
				marshalJSON(t, map[string]interface{}{
					"type": "m.typing", "room_id": roomID, "content": map[string]interface{}{
						"user_ids": []string{bob},
					},
				}),
			},
		},
	}
	v2ServerChan <- v2Resp
	time.Sleep(10 * time.Millisecond)

	// 2nd request with no special args should remember we want the typing notif
	w = httptest.NewRecorder()
	w.Body = bytes.NewBuffer(nil)
	req = httptest.NewRequest("POST", "/_matrix/client/v3/sync?since="+resp.Next, bytes.NewBuffer([]byte(`{}`)))
	req.Header.Set("Authorization", aliceBearer)
	server.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("/v3/sync returned HTTP %d want 200", w.Code)
	}

	// Check that the response returns bob typing
	resp = parseResponse(t, w.Body)
	if resp.Typing == nil {
		t.Fatalf("no typing response, wanted one")
	}
	if len(resp.Typing.UserIDs) != 1 {
		t.Fatalf("typing got %d users, want 1: %v", len(resp.Typing.UserIDs), resp.Typing.UserIDs)
	}
	if resp.Typing.UserIDs[0] != bob {
		t.Fatalf("typing got %s want %s", resp.Typing.UserIDs[0], bob)
	}
}
