package syncv3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/handler"
	"github.com/matrix-org/sync-v3/testutils"
	"github.com/matrix-org/sync-v3/testutils/m"
	"github.com/tidwall/gjson"
)

// Integration tests for the sync-v3 server

const (
	alice      = "@alice:localhost"
	aliceToken = "ALICE_BEARER_TOKEN"
	bob        = "@bob:localhost"
	bobToken   = "BOB_BEARER_TOKEN"
)

type testV2Server struct {
	mu          *sync.Mutex
	tokenToUser map[string]string
	queues      map[string]chan sync2.SyncResponse
	waiting     map[string]*sync.Cond // broadcasts when the server is about to read a blocking input
	srv         *httptest.Server
}

func (s *testV2Server) addAccount(userID, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenToUser[token] = userID
	s.queues[userID] = make(chan sync2.SyncResponse, 100)
	s.waiting[userID] = &sync.Cond{
		L: &sync.Mutex{},
	}
}

func (s *testV2Server) userID(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenToUser[token]
}

func (s *testV2Server) queueResponse(userID string, resp sync2.SyncResponse) {
	s.mu.Lock()
	ch := s.queues[userID]
	s.mu.Unlock()
	ch <- resp
	if !testutils.Quiet {
		log.Printf("testV2Server: enqueued v2 response for %s", userID)
	}
}

// blocks until nextResponse is called with an empty channel (that is, the server has caught up with v2 responses)
func (s *testV2Server) waitUntilEmpty(t *testing.T, userID string) {
	t.Helper()
	s.mu.Lock()
	cond := s.waiting[userID]
	s.mu.Unlock()
	cond.L.Lock()
	cond.Wait()
	cond.L.Unlock()
}

func (s *testV2Server) nextResponse(userID string) *sync2.SyncResponse {
	s.mu.Lock()
	ch := s.queues[userID]
	cond := s.waiting[userID]
	s.mu.Unlock()
	if ch == nil {
		log.Fatalf("testV2Server: nextResponse called with %s but there is no chan for this user", userID)
	}
	if len(ch) == 0 {
		// broadcast to tests (waitUntilEmpty) that we're going to block for new data.
		// We need to do it like this so we can make sure that the server has fully processed
		// the previous responses
		cond.Broadcast()
	}
	select {
	case data := <-ch:
		if !testutils.Quiet {
			log.Printf(
				"testV2Server: nextResponse %s returning data: [invite=%d,join=%d,leave=%d]",
				userID, len(data.Rooms.Invite), len(data.Rooms.Join), len(data.Rooms.Leave),
			)
		}
		return &data
	case <-time.After(1 * time.Second):
		if !testutils.Quiet {
			log.Printf("testV2Server: nextResponse %s waited >1s for data, returning null", userID)
		}
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

func runTestV2Server(t testutils.TestBenchInterface) *testV2Server {
	t.Helper()
	server := &testV2Server{
		tokenToUser: make(map[string]string),
		queues:      make(map[string]chan sync2.SyncResponse),
		waiting:     make(map[string]*sync.Cond),
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
	srv     *httptest.Server
	handler *handler.SyncLiveHandler
}

func (s *testV3Server) close() {
	s.srv.Close()
	s.handler.Teardown()
}

func (s *testV3Server) restart(t *testing.T, v2 *testV2Server, pq string) {
	t.Helper()
	log.Printf("restarting server")
	s.close()
	ss := runTestServer(t, v2, pq)
	s.srv = ss.srv
	v2.srv.CloseClientConnections() // kick-over v2 conns
}

func (s *testV3Server) mustDoV3Request(t testutils.TestBenchInterface, token string, reqBody sync3.Request) (respBody *sync3.Response) {
	t.Helper()
	return s.mustDoV3RequestWithPos(t, token, "", reqBody)
}

func (s *testV3Server) mustDoV3RequestWithPos(t testutils.TestBenchInterface, token string, pos string, reqBody sync3.Request) (respBody *sync3.Response) {
	t.Helper()
	resp, respBytes, code := s.doV3Request(t, context.Background(), token, pos, reqBody)
	if code != 200 {
		t.Fatalf("mustDoV3Request returned code %d body: %s", code, string(respBytes))
	}
	return resp
}

func (s *testV3Server) doV3Request(t testutils.TestBenchInterface, ctx context.Context, token string, pos string, reqBody sync3.Request) (respBody *sync3.Response, respBytes []byte, statusCode int) {
	t.Helper()
	j, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("cannot marshal request body as JSON: %s", err)
	}
	body := bytes.NewBuffer(j)
	qps := "?timeout="
	if reqBody.TimeoutMSecs() > 0 {
		qps += fmt.Sprintf("%d", reqBody.TimeoutMSecs())
	} else {
		qps += "20"
	}
	if pos != "" {
		qps += fmt.Sprintf("&pos=%s", pos)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.srv.URL+"/_matrix/client/v3/sync"+qps, body)
	if err != nil {
		t.Fatalf("failed to make NewRequest: %s", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.srv.Client().Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, 0
		}
		t.Fatalf("failed to Do request: %s", err)
	}
	defer resp.Body.Close()
	respBytes, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %s", err)
	}
	var r sync3.Response
	if err := json.Unmarshal(respBytes, &r); err != nil {
		t.Fatalf("failed to decode v3 response as JSON: %s\nresponse: %s", err, string(respBytes))
	}
	return &r, respBytes, resp.StatusCode
}

func runTestServer(t testutils.TestBenchInterface, v2Server *testV2Server, postgresConnectionString string) *testV3Server {
	t.Helper()
	if postgresConnectionString == "" {
		postgresConnectionString = testutils.PrepareDBConnectionString()
	}
	h, err := handler.NewSync3Handler(&sync2.HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: v2Server.url(),
	}, postgresConnectionString, os.Getenv("SYNCV3_SECRET"), true)
	if err != nil {
		t.Fatalf("cannot make v3 handler: %s", err)
	}
	r := mux.NewRouter()
	r.Handle("/_matrix/client/v3/sync", h)
	r.Handle("/_matrix/client/unstable/org.matrix.msc3575/sync", h)
	srv := httptest.NewServer(r)
	return &testV3Server{
		srv:     srv,
		handler: h,
	}
}

func createRoomState(t testutils.TestBenchInterface, creator string, baseTimestamp time.Time) []json.RawMessage {
	t.Helper()
	var pl gomatrixserverlib.PowerLevelContent
	pl.Defaults()
	pl.Users = map[string]int64{
		creator: 100,
	}
	// all with the same timestamp as they get made atomically
	return []json.RawMessage{
		testutils.NewStateEvent(t, "m.room.create", "", creator, map[string]interface{}{"creator": creator}, testutils.WithTimestamp(baseTimestamp)),
		testutils.NewJoinEvent(t, creator, testutils.WithTimestamp(baseTimestamp)),
		testutils.NewStateEvent(t, "m.room.power_levels", "", creator, pl, testutils.WithTimestamp(baseTimestamp)),
		testutils.NewStateEvent(t, "m.room.join_rules", "", creator, map[string]interface{}{"join_rule": "public"}, testutils.WithTimestamp(baseTimestamp)),
	}
}

type roomEvents struct {
	roomID    string
	name      string
	state     []json.RawMessage
	events    []json.RawMessage
	prevBatch string
}

func (re *roomEvents) getStateEvent(evType, stateKey string) json.RawMessage {
	for _, s := range append(re.state, re.events...) {
		m := gjson.ParseBytes(s)
		if m.Get("type").Str == evType && m.Get("state_key").Str == stateKey {
			return s
		}
	}
	fmt.Println("getStateEvent not found ", evType, stateKey)
	return nil
}

func (re *roomEvents) MatchRoom(roomID string, r sync3.Room, matchers ...m.RoomMatcher) error {
	if re.roomID != roomID {
		return fmt.Errorf("MatchRoom room id: got %s want %s", roomID, re.roomID)
	}
	return m.CheckRoom(r, matchers...)
}

func v2JoinTimeline(joinEvents ...roomEvents) map[string]sync2.SyncV2JoinResponse {
	result := make(map[string]sync2.SyncV2JoinResponse)
	for _, re := range joinEvents {
		var data sync2.SyncV2JoinResponse
		data.Timeline = sync2.TimelineResponse{
			Events: re.events,
		}
		if re.state != nil {
			data.State = sync2.EventsResponse{
				Events: re.state,
			}
		}
		if re.prevBatch != "" {
			data.Timeline.PrevBatch = re.prevBatch
		}
		result[re.roomID] = data
	}
	return result
}

func ptr(i int) *int {
	return &i
}
