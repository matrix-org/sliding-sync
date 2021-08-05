package syncv3

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3/streams"
	"github.com/matrix-org/sync-v3/testutils"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	postgresConnectionString = testutils.PrepareDBConnectionString("syncv3_test_main")
	exitCode := m.Run()
	os.Exit(exitCode)
}

func newSync3Server(t *testing.T) (http.Handler, *mockV2Client) {
	// disable colours in tests to make it display nicer in IDEs
	log := zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		NoColor:    true,
	})
	wrapper := hlog.NewHandler(log)
	cli := &mockV2Client{
		authHeaderToUser: make(map[string]string),
		userToChan:       make(map[string]chan *sync2.SyncResponse),
		mu:               &sync.Mutex{},
	}
	h := NewSyncV3Handler(cli, postgresConnectionString)
	return wrapper(h), cli
}

type mockV2Client struct {
	authHeaderToUser map[string]string
	userToChan       map[string]chan *sync2.SyncResponse
	mu               *sync.Mutex
}

func (c *mockV2Client) getUserIDFromAuthHeader(authHeader string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	userID, ok := c.authHeaderToUser[authHeader]
	return userID, ok
}

func (c *mockV2Client) DoSyncV2(authHeader, since string) (*sync2.SyncResponse, int, error) {
	userID, ok := c.getUserIDFromAuthHeader(authHeader)
	if !ok {
		return nil, 401, nil
	}
	c.mu.Lock()
	ch := c.userToChan[userID]
	c.mu.Unlock()
	if ch == nil {
		return nil, 500, nil
	}
	return <-ch, 200, nil
}
func (c *mockV2Client) WhoAmI(authHeader string) (string, error) {
	userID, ok := c.getUserIDFromAuthHeader(authHeader)
	if !ok {
		return "", fmt.Errorf("test: unknown authorization header")
	}
	return userID, nil
}

func (c *mockV2Client) v2StreamForUser(userID, authHeader string) chan *sync2.SyncResponse {
	c.mu.Lock()
	c.authHeaderToUser[authHeader] = userID
	ch, ok := c.userToChan[userID]
	if !ok {
		ch = make(chan *sync2.SyncResponse, 10)
		c.userToChan[userID] = ch
	}
	c.mu.Unlock()
	return ch
}

func marshalJSON(t *testing.T, in map[string]interface{}) json.RawMessage {
	t.Helper()
	j, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshalJSON: %s", err)
	}
	return j
}

func parseResponse(t *testing.T, body *bytes.Buffer) *streams.Response {
	t.Helper()
	var v3Resp streams.Response
	if err := json.Unmarshal(body.Bytes(), &v3Resp); err != nil {
		t.Fatalf("failed to unmarshal response: %s", err)
	}
	return &v3Resp
}

func doSync3Request(t *testing.T, server http.Handler, authHeader, since string, reqBody map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	w.Body = bytes.NewBuffer(nil)
	path := "/_matrix/client/v3/sync?timeout=1000"
	if since != "" {
		path += "&since=" + since
	}
	req := httptest.NewRequest("POST", path, bytes.NewBuffer(marshalJSON(t, reqBody)))
	req.Header.Set("Authorization", authHeader)
	server.ServeHTTP(w, req)
	t.Logf("POST /sync?since=%s (auth=%s) => HTTP %d", since, authHeader, w.Code)
	return w
}

func mustDoSync3Request(t *testing.T, server http.Handler, authHeader, since string, reqBody map[string]interface{}) *streams.Response {
	t.Helper()
	w := doSync3Request(t, server, authHeader, since, reqBody)
	if w.Code != 200 {
		t.Fatalf("mustDoSync3Request: got status %d want 200 : %s", w.Code, string(w.Body.Bytes()))
	}
	return parseResponse(t, w.Body)
}
