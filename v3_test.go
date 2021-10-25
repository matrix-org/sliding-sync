package syncv3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

// Integration tests for the sync-v3 server

type testV2Server struct {
	mu          *sync.Mutex
	tokenToUser map[string]string
	queues      map[string]chan sync2.SyncResponse
	srv         *httptest.Server
}

func (s *testV2Server) addAccount(userID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenToUser[token] = userID
}

func (s *testV2Server) userID(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenToUser[token]
}

func (s *testV2Server) queueResponse(userID string, resp sync2.SyncResponse) {
	s.mu.Lock()
	ch := s.queues[userID]
	if ch == nil {
		ch = make(chan sync2.SyncResponse, 100)
		s.queues[userID] = ch
	}
	s.mu.Unlock()
	ch <- resp
	log.Printf("testV2Server: enqueued v2 response for %s", userID)
}

func (s *testV2Server) nextResponse(userID string) *sync2.SyncResponse {
	s.mu.Lock()
	ch := s.queues[userID]
	s.mu.Unlock()
	if ch == nil {
		log.Fatalf("testV2Server: nextResponse called with %s but there is no chan for this user", userID)
	}
	select {
	case data := <-ch:
		log.Printf("testV2Server: nextResponse %s returning data", userID)
		return &data
	case <-time.After(1 * time.Second):
		log.Printf("testV2Server: nextResponse %s waited >1s for data, returning null", userID)
		return nil
	}
}

// TODO: queueDeviceResponse(token string)

func (s *testV2Server) url() string {
	return s.srv.URL
}

func (s *testV2Server) close() {
	s.srv.Close()
}

func runTestV2Server(t *testing.T) *testV2Server {
	t.Helper()
	server := &testV2Server{
		tokenToUser: make(map[string]string),
		queues:      make(map[string]chan sync2.SyncResponse),
		mu:          &sync.Mutex{},
	}
	r := mux.NewRouter()
	r.HandleFunc("/_matrix/client/r0/account/whoami", func(w http.ResponseWriter, req *http.Request) {
		userID := server.userID(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		if userID == "" {
			w.WriteHeader(403)
			return
		}
		w.WriteHeader(200)
		w.Write([]byte(fmt.Sprintf(`{"user_id":"%s"}`, userID)))
	})
	r.HandleFunc("/_matrix/client/r0/sync", func(w http.ResponseWriter, req *http.Request) {
		userID := server.userID(strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer "))
		if userID == "" {
			w.WriteHeader(403)
			return
		}
		resp := server.nextResponse(userID)
		body, err := json.Marshal(resp)
		if err != nil {
			w.WriteHeader(500)
			t.Errorf("failed to marshal response: %s", err)
			return
		}
		w.WriteHeader(200)
		w.Write(body)
	})
	srv := httptest.NewServer(r)
	server.srv = srv
	return server
}

type testV3Server struct {
	srv *httptest.Server
}

func (s *testV3Server) close() {
	s.srv.Close()
}

func (s *testV3Server) mustDoV3Request(t *testing.T, token string, reqBody interface{}) (respBody *sync3.Response) {
	t.Helper()
	resp, code := s.doV3Request(t, token, reqBody)
	if code != 200 {
		t.Fatalf("mustDoV3Request returned code %d", code)
	}
	return resp
}

func (s *testV3Server) doV3Request(t *testing.T, token string, reqBody interface{}) (respBody *sync3.Response, statusCode int) {
	t.Helper()
	var body io.Reader
	switch v := reqBody.(type) {
	case []byte:
		body = bytes.NewBuffer(v)
	case json.RawMessage:
		body = bytes.NewBuffer(v)
	case string:
		body = bytes.NewBufferString(v)
	default:
		j, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("cannot marshal request body as JSON: %s", err)
		}
		body = bytes.NewBuffer(j)
	}
	req, err := http.NewRequest("POST", s.srv.URL+"/_matrix/client/v3/sync", body)
	if err != nil {
		t.Fatalf("failed to make NewRequest: %s", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		t.Fatalf("failed to Do request: %s", err)
	}
	defer resp.Body.Close()
	respBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %s", err)
	}
	var r sync3.Response
	if err := json.Unmarshal(respBytes, &r); err != nil {
		t.Fatalf("failed to decode v3 response as JSON: %s\nresponse: %s", err, string(respBytes))
	}
	return &r, resp.StatusCode
}

func runTestServer(t *testing.T, v2Server *testV2Server) *testV3Server {
	t.Helper()
	postgresConnectionString := testutils.PrepareDBConnectionString("syncv3_test_sync3_integration")
	h, err := sync3.NewSync3Handler(&sync2.HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: v2Server.url(),
	}, postgresConnectionString)
	if err != nil {
		t.Fatalf("cannot make v3 handler: %s", err)
	}
	r := mux.NewRouter()
	r.Handle("/_matrix/client/v3/sync", h)
	srv := httptest.NewServer(r)
	return &testV3Server{
		srv: srv,
	}
}

func TestInteg(t *testing.T) {
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2)
	defer v2.close()
	defer v3.close()
	alice := "@alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN"
	roomA := "!a:localhost"
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: map[string]sync2.SyncV2JoinResponse{
				roomA: {
					Timeline: sync2.TimelineResponse{
						Events: []json.RawMessage{
							testutils.NewStateEvent(t, "m.room.create", "", alice, map[string]interface{}{"creator": alice}),
							testutils.NewStateEvent(t, "m.room.member", alice, alice, map[string]interface{}{"membership": "join"}),
							testutils.NewStateEvent(t, "m.room.join_rules", "", alice, map[string]interface{}{"join_rule": "public"}),
							testutils.NewEvent(t, "m.room.message", alice, map[string]interface{}{"body": "hello world"}, int64(gomatrixserverlib.AsTimestamp(time.Now()))),
						},
					},
				},
			},
		},
	})

	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Rooms: sync3.SliceRanges{
			[2]int64{0, 9}, // first 10 rooms
		},
	})
	if res.Count != 1 {
		t.Fatalf("want count=1, got count=%d", res.Count)
	}
	if len(res.Ops) != 1 {
		t.Fatalf("want 1 op, got %d", len(res.Ops))
	}
	op := res.Ops[0]
	if op.Op() != sync3.OpSync {
		t.Fatalf("want op %s got %s", sync3.OpSync, op.Op())
	}
	opRange := op.(*sync3.ResponseOpRange)
	if len(opRange.Rooms) != 1 {
		t.Fatalf("want 1 room, got %d", len(opRange.Rooms))
	}
	room := opRange.Rooms[0]
	if room.RoomID != roomA {
		t.Fatalf("want room id %s got %s", roomA, room.RoomID)
	}

}
