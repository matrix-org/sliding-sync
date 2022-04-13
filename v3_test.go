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
	"reflect"
	"sort"
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
	}, postgresConnectionString, true)
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
		testutils.NewStateEvent(t, "m.room.member", creator, creator, map[string]interface{}{"membership": "join"}, testutils.WithTimestamp(baseTimestamp)),
		testutils.NewStateEvent(t, "m.room.power_levels", "", creator, pl, testutils.WithTimestamp(baseTimestamp)),
		testutils.NewStateEvent(t, "m.room.join_rules", "", creator, map[string]interface{}{"join_rule": "public"}, testutils.WithTimestamp(baseTimestamp)),
	}
}

type roomMatcher func(r sync3.Room) error

func MatchRoomID(id string) roomMatcher {
	return func(r sync3.Room) error {
		if id == "" {
			return nil
		}
		if r.RoomID != id {
			return fmt.Errorf("MatchRoomID: mismatch, got %s want %s", r.RoomID, id)
		}
		return nil
	}
}

func MatchRoomName(name string) roomMatcher {
	return func(r sync3.Room) error {
		if name == "" {
			return nil
		}
		if r.Name != name {
			return fmt.Errorf("name mismatch, got %s want %s", r.Name, name)
		}
		return nil
	}
}
func MatchRoomRequiredState(events []json.RawMessage) roomMatcher {
	return func(r sync3.Room) error {
		if len(r.RequiredState) != len(events) {
			return fmt.Errorf("required state length mismatch, got %d want %d", len(r.RequiredState), len(events))
		}
		// allow any ordering for required state
		for _, want := range events {
			found := false
			for _, got := range r.RequiredState {
				if bytes.Equal(got, want) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("required state want event %v but it does not exist", string(want))
			}
		}
		return nil
	}
}
func MatchRoomInviteState(events []json.RawMessage) roomMatcher {
	return func(r sync3.Room) error {
		if len(r.InviteState) != len(events) {
			return fmt.Errorf("invite state length mismatch, got %d want %d", len(r.InviteState), len(events))
		}
		// allow any ordering for required state
		for _, want := range events {
			found := false
			for _, got := range r.InviteState {
				if bytes.Equal(got, want) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("required state want event %v but it does not exist", string(want))
			}
		}
		return nil
	}
}

// Similar to MatchRoomTimeline but takes the last n events of `events` and only checks with the last
// n events of the timeline.
func MatchRoomTimelineMostRecent(n int, events []json.RawMessage) roomMatcher {
	subset := events[len(events)-n:]
	return func(r sync3.Room) error {
		if len(r.Timeline) < len(subset) {
			return fmt.Errorf("timeline length mismatch: got %d want at least %d", len(r.Timeline), len(subset))
		}
		gotSubset := r.Timeline[len(r.Timeline)-n:]
		for i := range gotSubset {
			if !bytes.Equal(gotSubset[i], subset[i]) {
				return fmt.Errorf("timeline[%d]\ngot  %v \nwant %v", i, string(r.Timeline[i]), string(events[i]))
			}
		}
		return nil
	}
}

func MatchRoomPrevBatch(prevBatch string) roomMatcher {
	return func(r sync3.Room) error {
		if prevBatch != r.PrevBatch {
			return fmt.Errorf("MatchRoomPrevBatch: got %v want %v", r.PrevBatch, prevBatch)
		}
		return nil
	}
}

// Match the timeline with exactly these events in exactly this order
func MatchRoomTimeline(events []json.RawMessage) roomMatcher {
	return func(r sync3.Room) error {
		if len(r.Timeline) != len(events) {
			return fmt.Errorf("timeline length mismatch: got %d want %d", len(r.Timeline), len(events))
		}
		for i := range r.Timeline {
			if !bytes.Equal(r.Timeline[i], events[i]) {
				return fmt.Errorf("timeline[%d]\ngot  %v \nwant %v", i, string(r.Timeline[i]), string(events[i]))
			}
		}
		return nil
	}
}
func MatchRoomHighlightCount(count int64) roomMatcher {
	return func(r sync3.Room) error {
		if r.HighlightCount != count {
			return fmt.Errorf("highlight count mismatch, got %d want %d", r.HighlightCount, count)
		}
		return nil
	}
}
func MatchRoomNotificationCount(count int64) roomMatcher {
	return func(r sync3.Room) error {
		if r.NotificationCount != count {
			return fmt.Errorf("notification count mismatch, got %d want %d", r.NotificationCount, count)
		}
		return nil
	}
}

func MatchRoomInitial(initial bool) roomMatcher {
	return func(r sync3.Room) error {
		if r.Initial != initial {
			return fmt.Errorf("MatchRoomInitial: got %v want %v", r.Initial, initial)
		}
		return nil
	}
}

type roomEvents struct {
	roomID    string
	name      string
	state     []json.RawMessage
	events    []json.RawMessage
	prevBatch string
}

func (re *roomEvents) MatchRoom(r sync3.Room, matchers ...roomMatcher) error {
	if re.roomID != r.RoomID {
		return fmt.Errorf("MatchRoom room id: got %s want %s", r.RoomID, re.roomID)
	}
	for _, m := range matchers {
		if err := m(r); err != nil {
			return fmt.Errorf("MatchRoom %s : %s", r.RoomID, err)
		}
	}
	return nil
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

type respMatcher func(res *sync3.Response) error
type opMatcher func(op sync3.ResponseOp) error
type rangeMatcher func(op sync3.ResponseOpRange) error

func MatchV3Count(wantCount int) respMatcher {
	return MatchV3Counts([]int{wantCount})
}
func MatchV3Counts(wantCounts []int) respMatcher {
	return func(res *sync3.Response) error {
		if !reflect.DeepEqual(res.Counts, wantCounts) {
			return fmt.Errorf("counts: got %v want %v", res.Counts, wantCounts)
		}
		return nil
	}
}

func MatchRoomSubscriptions(strictLength bool, wantSubs map[string][]roomMatcher) respMatcher {
	return func(res *sync3.Response) error {
		if strictLength && len(res.RoomSubscriptions) != len(wantSubs) {
			return fmt.Errorf("MatchRoomSubscriptions: strict length on: got %v subs want %v", len(res.RoomSubscriptions), len(wantSubs))
		}
		for roomID, matchers := range wantSubs {
			room, ok := res.RoomSubscriptions[roomID]
			if !ok {
				return fmt.Errorf("MatchRoomSubscriptions: want sub for %s but it was missing", roomID)
			}
			for _, m := range matchers {
				if err := m(room); err != nil {
					return fmt.Errorf("MatchRoomSubscriptions: %s", err)
				}
			}
		}
		return nil
	}
}

func MatchOTKCounts(otkCounts map[string]int) respMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.E2EE == nil {
			return fmt.Errorf("MatchOTKCounts: no E2EE extension present")
		}
		if !reflect.DeepEqual(res.Extensions.E2EE.OTKCounts, otkCounts) {
			return fmt.Errorf("MatchOTKCounts: got %v want %v", res.Extensions.E2EE.OTKCounts, otkCounts)
		}
		return nil
	}
}

func MatchDeviceLists(changed, left []string) respMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.E2EE == nil {
			return fmt.Errorf("MatchDeviceLists: no E2EE extension present")
		}
		if res.Extensions.E2EE.DeviceLists == nil {
			return fmt.Errorf("MatchDeviceLists: no device lists present")
		}
		if !reflect.DeepEqual(res.Extensions.E2EE.DeviceLists.Changed, changed) {
			return fmt.Errorf("MatchDeviceLists: got changed: %v want %v", res.Extensions.E2EE.DeviceLists.Changed, changed)
		}
		if !reflect.DeepEqual(res.Extensions.E2EE.DeviceLists.Left, left) {
			return fmt.Errorf("MatchDeviceLists: got left: %v want %v", res.Extensions.E2EE.DeviceLists.Left, left)
		}
		return nil
	}
}

func MatchToDeviceMessages(wantMsgs []json.RawMessage) respMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.ToDevice == nil {
			return fmt.Errorf("MatchToDeviceMessages: missing to_device extension")
		}
		if len(res.Extensions.ToDevice.Events) != len(wantMsgs) {
			return fmt.Errorf("MatchToDeviceMessages: got %d events, want %d", len(res.Extensions.ToDevice.Events), len(wantMsgs))
		}
		for i := 0; i < len(wantMsgs); i++ {
			if !reflect.DeepEqual(res.Extensions.ToDevice.Events[i], wantMsgs[i]) {
				return fmt.Errorf("MatchToDeviceMessages[%d]: got %v want %v", i, string(res.Extensions.ToDevice.Events[i]), string(wantMsgs[i]))
			}
		}
		return nil
	}
}

func MatchRoomRange(rooms ...[]roomMatcher) rangeMatcher {
	return func(op sync3.ResponseOpRange) error {
		if len(rooms) != len(op.Rooms) {
			return fmt.Errorf("MatchRoomRange: length of params must match ordering of rooms in range response. Got %v params want %v", len(rooms), len(op.Rooms))
		}
		for i, matchers := range rooms {
			room := op.Rooms[i]
			for _, m := range matchers {
				if err := m(room); err != nil {
					return err
				}
			}
		}
		return nil
	}
}

func MatchV3SyncOpWithMatchers(matchers ...rangeMatcher) opMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpSync {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpSync)
		}
		oper := op.(*sync3.ResponseOpRange)
		for _, m := range matchers {
			if err := m(*oper); err != nil {
				return err
			}
		}
		return nil
	}
}

func MatchV3SyncOp(fn func(op *sync3.ResponseOpRange) error) opMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpSync {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpSync)
		}
		oper := op.(*sync3.ResponseOpRange)
		return fn(oper)
	}
}

func MatchV3InsertOp(listIndex, roomIndex int, roomID string, matchers ...roomMatcher) opMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpInsert {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpInsert)
		}
		oper := op.(*sync3.ResponseOpSingle)
		if oper.List != listIndex {
			return fmt.Errorf("%s: got list index %d want %d", sync3.OpInsert, oper.List, listIndex)
		}
		if *oper.Index != roomIndex {
			return fmt.Errorf("%s: got index %d want %d", sync3.OpInsert, *oper.Index, roomIndex)
		}
		if oper.Room.RoomID != roomID {
			return fmt.Errorf("%s: got %s want %s", sync3.OpInsert, oper.Room.RoomID, roomID)
		}
		for _, m := range matchers {
			if err := m(*oper.Room); err != nil {
				return err
			}
		}
		return nil
	}
}

func MatchV3UpdateOp(listIndex, roomIndex int, roomID string, matchers ...roomMatcher) opMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpUpdate {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpUpdate)
		}
		oper := op.(*sync3.ResponseOpSingle)
		if oper.List != listIndex {
			return fmt.Errorf("%s: got list index %d want %d", sync3.OpUpdate, oper.List, listIndex)
		}
		if *oper.Index != roomIndex {
			return fmt.Errorf("%s: got room index %d want %d", sync3.OpUpdate, *oper.Index, roomIndex)
		}
		if oper.Room.RoomID != roomID {
			return fmt.Errorf("%s: got %s want %s", sync3.OpUpdate, oper.Room.RoomID, roomID)
		}
		for _, m := range matchers {
			if err := m(*oper.Room); err != nil {
				return err
			}
		}
		return nil
	}
}

func MatchV3DeleteOp(listIndex, roomIndex int) opMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpDelete {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpDelete)
		}
		oper := op.(*sync3.ResponseOpSingle)
		if *oper.Index != roomIndex {
			return fmt.Errorf("%s: got room index %d want %d", sync3.OpDelete, *oper.Index, roomIndex)
		}
		if oper.List != listIndex {
			return fmt.Errorf("%s: got list index %d want %d", sync3.OpDelete, oper.List, listIndex)
		}
		return nil
	}
}

func MatchV3InvalidateOp(listIndex int, start, end int64) opMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpInvalidate {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpInvalidate)
		}
		oper := op.(*sync3.ResponseOpRange)
		if oper.Range[0] != start {
			return fmt.Errorf("%s: got start %d want %d", sync3.OpInvalidate, oper.Range[0], start)
		}
		if oper.Range[1] != end {
			return fmt.Errorf("%s: got end %d want %d", sync3.OpInvalidate, oper.Range[1], end)
		}
		if oper.List != listIndex {
			return fmt.Errorf("%s: got list index %d want %d", sync3.OpInvalidate, oper.List, listIndex)
		}
		return nil
	}
}

func MatchV3Ops(matchOps ...opMatcher) respMatcher {
	return func(res *sync3.Response) error {
		if len(matchOps) != len(res.Ops) {
			return fmt.Errorf("ops: got %d ops want %d", len(res.Ops), len(matchOps))
		}
		for i := range res.Ops {
			op := res.Ops[i]
			if err := matchOps[i](op); err != nil {
				return fmt.Errorf("op[%d](%s) - %s", i, op.Op(), err)
			}
		}
		return nil
	}
}

func MatchAccountData(globals []json.RawMessage, rooms map[string][]json.RawMessage) respMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.AccountData == nil {
			return fmt.Errorf("MatchAccountData: no account_data extension")
		}
		if len(globals) > 0 {
			if err := equalAnyOrder(res.Extensions.AccountData.Global, globals); err != nil {
				return fmt.Errorf("MatchAccountData[global]: %s", err)
			}
		}
		if len(rooms) > 0 {
			if len(rooms) != len(res.Extensions.AccountData.Rooms) {
				return fmt.Errorf("MatchAccountData: got %d rooms with account data, want %d", len(res.Extensions.AccountData.Rooms), len(rooms))
			}
			for roomID := range rooms {
				gots := res.Extensions.AccountData.Rooms[roomID]
				if gots == nil {
					return fmt.Errorf("MatchAccountData: want room account data for %s but it was missing", roomID)
				}
				if err := equalAnyOrder(gots, rooms[roomID]); err != nil {
					return fmt.Errorf("MatchAccountData[room]: %s", err)
				}
			}
		}
		return nil
	}
}

func MatchResponse(t *testing.T, res *sync3.Response, matchers ...respMatcher) {
	t.Helper()
	for _, m := range matchers {
		err := m(res)
		if err != nil {
			b, _ := json.Marshal(res)
			t.Errorf("MatchResponse: %s\n%+v", err, string(b))
		}
	}
}

func ptr(i int) *int {
	return &i
}

func equalAnyOrder(got, want []json.RawMessage) error {
	if len(got) != len(want) {
		return fmt.Errorf("equalAnyOrder: got %d, want %d", len(got), len(want))
	}
	sort.Slice(got, func(i, j int) bool {
		return string(got[i]) < string(got[j])
	})
	sort.Slice(want, func(i, j int) bool {
		return string(want[i]) < string(want[j])
	})
	for i := range got {
		if !reflect.DeepEqual(got[i], want[i]) {
			return fmt.Errorf("equalAnyOrder: [%d] got %v want %v", i, string(got[i]), string(want[i]))
		}
	}
	return nil
}
