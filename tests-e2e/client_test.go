package syncv3_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1" // nolint:gosec
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/tidwall/gjson"
)

const (
	SharedSecret = "complement"
)

var (
	boolTrue  = true
	boolFalse = false
)

type Event struct {
	ID       string                 `json:"event_id"`
	Type     string                 `json:"type"`
	Sender   string                 `json:"sender"`
	StateKey *string                `json:"state_key"`
	Content  map[string]interface{} `json:"content"`

	/* The following fields are ignored in blueprints as clients are unable to set them.
	 * They are used with federation.Server.
	 */

	Unsigned map[string]interface{} `json:"unsigned"`

	// The events needed to authenticate this event.
	// This can be either []EventReference for room v1/v2, or []string for room v3 onwards.
	// If it is left at nil, MustCreateEvent will populate it automatically based on the room state.
	AuthEvents interface{} `json:"auth_events"`

	// The prev events of the event if we want to override or falsify them.
	// If it is left at nil, MustCreateEvent will populate it automatically based on the forward extremities.
	PrevEvents interface{} `json:"prev_events"`

	// If this is a redaction, the event that it redacts
	Redacts string `json:"redacts"`
}

func NewEncryptionEvent() Event {
	return Event{
		Type:     "m.room.encryption",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"algorithm":            "m.megolm.v1.aes-sha2",
			"rotation_period_ms":   604800000,
			"rotation_period_msgs": 100,
		},
	}
}

type MessagesBatch struct {
	Chunk []json.RawMessage `json:"chunk"`
	Start string            `json:"start"`
	End   string            `json:"end"`
}

type CtxKey string

const (
	CtxKeyWithRetryUntil CtxKey = "complement_retry_until" // contains *retryUntilParams
)

type retryUntilParams struct {
	timeout time.Duration
	untilFn func(*http.Response) bool
}

// RequestOpt is a functional option which will modify an outgoing HTTP request.
// See functions starting with `With...` in this package for more info.
type RequestOpt func(req *http.Request)

// SyncCheckOpt is a functional option for use with MustSyncUntil which should return <nil> if
// the response satisfies the check, else return a human friendly error.
// The result object is the entire /sync response from this request.
type SyncCheckOpt func(clientUserID string, topLevelSyncJSON gjson.Result) error

// SyncReq contains all the /sync request configuration options. The empty struct `SyncReq{}` is valid
// which will do a full /sync due to lack of a since token.
type SyncReq struct {
	// A point in time to continue a sync from. This should be the next_batch token returned by an
	// earlier call to this endpoint.
	Since string
	// The ID of a filter created using the filter API or a filter JSON object encoded as a string.
	// The server will detect whether it is an ID or a JSON object by whether the first character is
	// a "{" open brace. Passing the JSON inline is best suited to one off requests. Creating a
	// filter using the filter API is recommended for clients that reuse the same filter multiple
	// times, for example in long poll requests.
	Filter string
	// Controls whether to include the full state for all rooms the user is a member of.
	// If this is set to true, then all state events will be returned, even if since is non-empty.
	// The timeline will still be limited by the since parameter. In this case, the timeout parameter
	// will be ignored and the query will return immediately, possibly with an empty timeline.
	// If false, and since is non-empty, only state which has changed since the point indicated by
	// since will be returned.
	// By default, this is false.
	FullState bool
	// Controls whether the client is automatically marked as online by polling this API. If this
	// parameter is omitted then the client is automatically marked as online when it uses this API.
	// Otherwise if the parameter is set to “offline” then the client is not marked as being online
	// when it uses this API. When set to “unavailable”, the client is marked as being idle.
	// One of: [offline online unavailable].
	SetPresence string
	// The maximum time to wait, in milliseconds, before returning this request. If no events
	// (or other data) become available before this time elapses, the server will return a response
	// with empty fields.
	// By default, this is 1000 for Complement testing.
	TimeoutMillis string // string for easier conversion to query params
}

type CSAPI struct {
	UserID      string
	Localpart   string
	AccessToken string
	DeviceID    string
	BaseURL     string
	Client      *http.Client
	// how long are we willing to wait for MustSyncUntil.... calls
	SyncUntilTimeout time.Duration
	// True to enable verbose logging
	Debug bool

	txnID int
}

// UploadContent uploads the provided content with an optional file name. Fails the test on error. Returns the MXC URI.
func (c *CSAPI) UploadContent(t *testing.T, fileBody []byte, fileName string, contentType string) string {
	t.Helper()
	query := url.Values{}
	if fileName != "" {
		query.Set("filename", fileName)
	}
	res := c.MustDoFunc(
		t, "POST", []string{"_matrix", "media", "v3", "upload"},
		WithRawBody(fileBody), WithContentType(contentType), WithQueries(query),
	)
	body := ParseJSON(t, res)
	return GetJSONFieldStr(t, body, "content_uri")
}

// DownloadContent downloads media from the server, returning the raw bytes and the Content-Type. Fails the test on error.
func (c *CSAPI) DownloadContent(t *testing.T, mxcUri string) ([]byte, string) {
	t.Helper()
	origin, mediaId := SplitMxc(mxcUri)
	res := c.MustDo(t, "GET", []string{"_matrix", "media", "v3", "download", origin, mediaId}, struct{}{})
	contentType := res.Header.Get("Content-Type")
	b, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Error(err)
	}
	return b, contentType
}

// CreateRoom creates a room with an optional HTTP request body. Fails the test on error. Returns the room ID.
func (c *CSAPI) CreateRoom(t *testing.T, creationContent interface{}) string {
	t.Helper()
	res := c.MustDo(t, "POST", []string{"_matrix", "client", "v3", "createRoom"}, creationContent)
	body := ParseJSON(t, res)
	return GetJSONFieldStr(t, body, "room_id")
}

// JoinRoom joins the room ID or alias given, else fails the test. Returns the room ID.
func (c *CSAPI) JoinRoom(t *testing.T, roomIDOrAlias string, serverNames []string) string {
	t.Helper()
	// construct URL query parameters
	query := make(url.Values, len(serverNames))
	for _, serverName := range serverNames {
		query.Add("server_name", serverName)
	}
	// join the room
	res := c.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "join", roomIDOrAlias}, WithQueries(query))
	// return the room ID if we joined with it
	if roomIDOrAlias[0] == '!' {
		return roomIDOrAlias
	}
	// otherwise we should be told the room ID if we joined via an alias
	body := ParseJSON(t, res)
	return GetJSONFieldStr(t, body, "room_id")
}

// KnockRoom knocks on the room with the given room ID or alias given, else fails the
// test. Returns the room ID.
func (c *CSAPI) KnockRoom(t *testing.T, roomIDOrAlias string, serverNames []string) string {
	t.Helper()
	// construct URL query parameters
	query := make(url.Values, len(serverNames))
	for _, serverName := range serverNames {
		query.Add("server_name", serverName)
	}
	// join the room
	res := c.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "knock", roomIDOrAlias}, WithQueries(query), WithRawBody([]byte("{}")))
	// return the room ID if we joined with it
	if roomIDOrAlias[0] == '!' {
		return roomIDOrAlias
	}
	// otherwise we should be told the room ID if we joined via an alias
	body := ParseJSON(t, res)
	return GetJSONFieldStr(t, body, "room_id")
}

func (c *CSAPI) SendTyping(t *testing.T, roomID string, isTyping bool, durMs int) {
	t.Helper()
	c.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "rooms", roomID, "typing", c.UserID}, WithJSONBody(t, map[string]interface{}{
		"timeout": durMs,
		"typing":  isTyping,
	}))
}

// LeaveRoom joins the room ID, else fails the test.
func (c *CSAPI) LeaveRoom(t *testing.T, roomID string) {
	t.Helper()
	// leave the room
	c.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "leave"})
}

// InviteRoom invites userID to the room ID, else fails the test.
func (c *CSAPI) InviteRoom(t *testing.T, roomID string, userID string) {
	t.Helper()
	// Invite the user to the room
	body := map[string]interface{}{
		"user_id": userID,
	}
	c.MustDo(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "invite"}, body)
}

func (c *CSAPI) GetGlobalAccountData(t *testing.T, eventType string) *http.Response {
	return c.MustDoFunc(t, "GET", []string{"_matrix", "client", "v3", "user", c.UserID, "account_data", eventType})
}

func (c *CSAPI) SetGlobalAccountData(t *testing.T, eventType string, content map[string]interface{}) *http.Response {
	return c.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "user", c.UserID, "account_data", eventType}, WithJSONBody(t, content))
}

func (c *CSAPI) SetRoomAccountData(t *testing.T, roomID, eventType string, content map[string]interface{}) *http.Response {
	return c.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "user", c.UserID, "rooms", roomID, "account_data", eventType}, WithJSONBody(t, content))
}

// SendEventUnsynced sends `e` into the room and returns the event ID of the sent event.
func (c *CSAPI) SendEventUnsynced(t *testing.T, roomID string, e Event) string {
	t.Helper()
	c.txnID++
	paths := []string{"_matrix", "client", "v3", "rooms", roomID, "send", e.Type, strconv.Itoa(c.txnID)}
	if e.StateKey != nil {
		paths = []string{"_matrix", "client", "v3", "rooms", roomID, "state", e.Type, *e.StateKey}
	}
	res := c.MustDo(t, "PUT", paths, e.Content)
	body := ParseJSON(t, res)
	eventID := GetJSONFieldStr(t, body, "event_id")
	return eventID
}

// SendEventSynced sends `e` into the room and waits for its event ID to come down /sync.
// NB: This is specifically v2 sync, not v3 sliding sync!!
// Returns the event ID of the sent event.
func (c *CSAPI) SendEventSynced(t *testing.T, roomID string, e Event) string {
	t.Helper()
	eventID := c.SendEventUnsynced(t, roomID, e)
	t.Logf("SendEventSynced waiting for event ID %s", eventID)
	c.MustSyncUntil(t, SyncReq{}, SyncTimelineHas(roomID, func(r gjson.Result) bool {
		return r.Get("event_id").Str == eventID
	}))
	return eventID
}

func (c *CSAPI) SendReceipt(t *testing.T, roomID, eventID, receiptType string) *http.Response {
	return c.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "read_markers"}, WithJSONBody(t, map[string]interface{}{
		receiptType: eventID,
	}))
}

// Perform a single /sync request with the given request options. To sync until something happens,
// see `MustSyncUntil`.
//
// Fails the test if the /sync request does not return 200 OK.
// Returns the top-level parsed /sync response JSON as well as the next_batch token from the response.
func (c *CSAPI) MustSync(t *testing.T, syncReq SyncReq) (gjson.Result, string) {
	t.Helper()
	query := url.Values{
		"timeout": []string{"1000"},
	}
	// configure the HTTP request based on SyncReq
	if syncReq.TimeoutMillis != "" {
		query["timeout"] = []string{syncReq.TimeoutMillis}
	}
	if syncReq.Since != "" {
		query["since"] = []string{syncReq.Since}
	}
	if syncReq.Filter != "" {
		query["filter"] = []string{syncReq.Filter}
	}
	if syncReq.FullState {
		query["full_state"] = []string{"true"}
	}
	if syncReq.SetPresence != "" {
		query["set_presence"] = []string{syncReq.SetPresence}
	}
	res := c.MustDoFunc(t, "GET", []string{"_matrix", "client", "v3", "sync"}, WithQueries(query))
	body := ParseJSON(t, res)
	result := gjson.ParseBytes(body)
	nextBatch := GetJSONFieldStr(t, body, "next_batch")
	return result, nextBatch
}

// MustSyncUntil blocks and continually calls /sync (advancing the since token) until all the
// check functions return no error. Returns the final/latest since token.
//
// Initial /sync example: (no since token)
//
//	bob.InviteRoom(t, roomID, alice.UserID)
//	alice.JoinRoom(t, roomID, nil)
//	alice.MustSyncUntil(t, client.SyncReq{}, client.SyncJoinedTo(alice.UserID, roomID))
//
// Incremental /sync example: (test controls since token)
//
//	since := alice.MustSyncUntil(t, client.SyncReq{TimeoutMillis: "0"}) // get a since token
//	bob.InviteRoom(t, roomID, alice.UserID)
//	since = alice.MustSyncUntil(t, client.SyncReq{Since: since}, client.SyncInvitedTo(alice.UserID, roomID))
//	alice.JoinRoom(t, roomID, nil)
//	alice.MustSyncUntil(t, client.SyncReq{Since: since}, client.SyncJoinedTo(alice.UserID, roomID))
//
// Checking multiple parts of /sync:
//
//	alice.MustSyncUntil(
//	    t, client.SyncReq{},
//	    client.SyncJoinedTo(alice.UserID, roomID),
//	    client.SyncJoinedTo(alice.UserID, roomID2),
//	    client.SyncJoinedTo(alice.UserID, roomID3),
//	)
//
// Check functions are unordered and independent. Once a check function returns true it is removed
// from the list of checks and won't be called again.
//
// In the unlikely event that you want all the checkers to pass *explicitly* in a single /sync
// response (e.g to assert some form of atomic update which updates multiple parts of the /sync
// response at once) then make your own checker function which does this.
//
// In the unlikely event that you need ordering on your checks, call MustSyncUntil multiple times
// with a single checker, and reuse the returned since token, as in the "Incremental sync" example.
//
// Will time out after CSAPI.SyncUntilTimeout. Returns the `next_batch` token from the final
// response.
func (c *CSAPI) MustSyncUntil(t *testing.T, syncReq SyncReq, checks ...SyncCheckOpt) string {
	t.Helper()
	start := time.Now()
	numResponsesReturned := 0
	checkers := make([]struct {
		check SyncCheckOpt
		errs  []string
	}, len(checks))
	for i := range checks {
		c := checkers[i]
		c.check = checks[i]
		checkers[i] = c
	}
	printErrors := func() string {
		err := "Checkers:\n"
		for _, c := range checkers {
			err += strings.Join(c.errs, "\n")
			err += ", \n"
		}
		return err
	}
	for {
		if time.Since(start) > c.SyncUntilTimeout {
			t.Fatalf("%s MustSyncUntil: timed out after %v. Seen %d /sync responses. %s", c.UserID, time.Since(start), numResponsesReturned, printErrors())
		}
		response, nextBatch := c.MustSync(t, syncReq)
		syncReq.Since = nextBatch
		numResponsesReturned += 1

		for i := 0; i < len(checkers); i++ {
			err := checkers[i].check(c.UserID, response)
			if err == nil {
				// check passed, removed from checkers
				checkers = append(checkers[:i], checkers[i+1:]...)
				i--
			} else {
				c := checkers[i]
				c.errs = append(c.errs, fmt.Sprintf("[t=%v] Response #%d: %s", time.Since(start), numResponsesReturned, err))
				checkers[i] = c
			}
		}
		if len(checkers) == 0 {
			// every checker has passed!
			time.Sleep(10 * time.Millisecond) // sleep a very small amount to give the proxy time to process data
			return syncReq.Since
		}
	}
}

// RegisterUser will register the user with given parameters and
// return user ID & access token, and fail the test on network error
func (c *CSAPI) RegisterUser(t *testing.T, localpart, password string) (userID, accessToken, deviceID string) {
	t.Helper()
	reqBody := map[string]interface{}{
		"auth": map[string]string{
			"type": "m.login.dummy",
		},
		"username": localpart,
		"password": password,
	}
	res := c.MustDo(t, "POST", []string{"_matrix", "client", "v3", "register"}, reqBody)

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("unable to read response body: %v", err)
	}

	userID = gjson.GetBytes(body, "user_id").Str
	accessToken = gjson.GetBytes(body, "access_token").Str
	deviceID = gjson.GetBytes(body, "device_id").Str
	return userID, accessToken, deviceID
}

// LoginUser will create a new device for the given user.
// The new access token and device ID are overwrite those of the current CSAPI instance.
func (c *CSAPI) Login(t *testing.T, password, deviceID string) {
	t.Helper()
	reqBody := map[string]interface{}{
		"type":     "m.login.password",
		"password": password,
		"identifier": map[string]interface{}{
			"type": "m.id.user",
			"user": c.UserID,
		},
		"device_id": deviceID,
	}
	res := c.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "login"}, WithJSONBody(t, reqBody))

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("unable to read response body: %v", err)
	}

	userID := gjson.GetBytes(body, "user_id").Str
	if c.UserID != userID {
		t.Fatalf("Logged in as %s but response included user_id=%s", c.UserID, userID)
	}
	gotDeviceID := gjson.GetBytes(body, "device_id").Str
	if gotDeviceID != deviceID {
		t.Fatalf("Asked for device ID %s but got %s", deviceID, gotDeviceID)
	}
	accessToken := gjson.GetBytes(body, "access_token").Str
	if c.AccessToken == accessToken {
		t.Fatalf("Logged in as %s but access token did not change (still %s)", c.UserID, c.AccessToken)
	}

	c.AccessToken = accessToken
	c.DeviceID = deviceID
}

// SetState PUTs a piece of state in a room and returns the event ID of the created state event.
func (c *CSAPI) SetState(t *testing.T, roomID, eventType, stateKey string, content map[string]interface{}) string {
	t.Helper()
	res := c.MustDoFunc(
		t,
		"PUT",
		[]string{"_matrix", "client", "v3", "rooms", roomID, "state", eventType, stateKey},
		WithJSONBody(t, content),
	)

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("unable to read response body: %v", err)
	}

	return gjson.ParseBytes(body).Get("event_id").Str
}

// RegisterSharedSecret registers a new account with a shared secret via HMAC
// See https://github.com/matrix-org/synapse/blob/e550ab17adc8dd3c48daf7fedcd09418a73f524b/synapse/_scripts/register_new_matrix_user.py#L40
func (c *CSAPI) RegisterSharedSecret(t *testing.T, user, pass string, isAdmin bool) (userID, accessToken, deviceID string) {
	resp := c.DoFunc(t, "GET", []string{"_synapse", "admin", "v1", "register"})
	if resp.StatusCode != 200 {
		t.Skipf("Homeserver image does not support shared secret registration, /_synapse/admin/v1/register returned HTTP %d", resp.StatusCode)
		return
	}
	body := ParseJSON(t, resp)
	nonce := gjson.GetBytes(body, "nonce")
	if !nonce.Exists() {
		t.Fatalf("Malformed shared secret GET response: %s", string(body))
	}
	mac := hmac.New(sha1.New, []byte(SharedSecret))
	mac.Write([]byte(nonce.Str))
	mac.Write([]byte("\x00"))
	mac.Write([]byte(user))
	mac.Write([]byte("\x00"))
	mac.Write([]byte(pass))
	mac.Write([]byte("\x00"))
	if isAdmin {
		mac.Write([]byte("admin"))
	} else {
		mac.Write([]byte("notadmin"))
	}
	sig := mac.Sum(nil)
	reqBody := map[string]interface{}{
		"nonce":    nonce.Str,
		"username": user,
		"password": pass,
		"mac":      hex.EncodeToString(sig),
		"admin":    isAdmin,
	}
	resp = c.MustDoFunc(t, "POST", []string{"_synapse", "admin", "v1", "register"}, WithJSONBody(t, reqBody))
	body = ParseJSON(t, resp)
	userID = gjson.GetBytes(body, "user_id").Str
	accessToken = gjson.GetBytes(body, "access_token").Str
	deviceID = gjson.GetBytes(body, "device_id").Str
	return userID, accessToken, deviceID
}

// GetCapbabilities queries the server's capabilities
func (c *CSAPI) GetCapabilities(t *testing.T) []byte {
	t.Helper()
	res := c.MustDoFunc(t, "GET", []string{"_matrix", "client", "v3", "capabilities"})
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("unable to read response body: %v", err)
	}
	return body
}

// GetDefaultRoomVersion returns the server's default room version
func (c *CSAPI) GetDefaultRoomVersion(t *testing.T) gomatrixserverlib.RoomVersion {
	t.Helper()
	capabilities := c.GetCapabilities(t)
	defaultVersion := gjson.GetBytes(capabilities, `capabilities.m\.room_versions.default`)
	if !defaultVersion.Exists() {
		// spec says use RoomV1 in this case
		return gomatrixserverlib.RoomVersionV1
	}

	return gomatrixserverlib.RoomVersion(defaultVersion.Str)
}

// MustDo will do the HTTP request and fail the test if the response is not 2xx
//
// Deprecated: Prefer MustDoFunc. MustDo is the older format which doesn't allow for vargs
// and will be removed in the future. MustDoFunc also logs HTTP response bodies on error.
func (c *CSAPI) MustDo(t *testing.T, method string, paths []string, jsonBody interface{}) *http.Response {
	t.Helper()
	res := c.DoFunc(t, method, paths, WithJSONBody(t, jsonBody))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		body, _ := ioutil.ReadAll(res.Body)
		t.Fatalf("CSAPI.MustDo %s %s returned HTTP %d : %s", method, res.Request.URL.String(), res.StatusCode, string(body))
	}
	return res
}

// WithRawBody sets the HTTP request body to `body`
func WithRawBody(body []byte) RequestOpt {
	return func(req *http.Request) {
		req.Body = ioutil.NopCloser(bytes.NewBuffer(body))
		// we need to manually set this because we don't set the body
		// in http.NewRequest due to using functional options, and only in NewRequest
		// does the stdlib set this for us.
		req.ContentLength = int64(len(body))
	}
}

// WithContentType sets the HTTP request Content-Type header to `cType`
func WithContentType(cType string) RequestOpt {
	return func(req *http.Request) {
		req.Header.Set("Content-Type", cType)
	}
}

// WithJSONBody sets the HTTP request body to the JSON serialised form of `obj`
func WithJSONBody(t *testing.T, obj interface{}) RequestOpt {
	return func(req *http.Request) {
		t.Helper()
		b, err := json.Marshal(obj)
		if err != nil {
			t.Fatalf("CSAPI.Do failed to marshal JSON body: %s", err)
		}
		WithRawBody(b)(req)
	}
}

// WithQueries sets the query parameters on the request.
// This function should not be used to set an "access_token" parameter for Matrix authentication.
// Instead, set CSAPI.AccessToken.
func WithQueries(q url.Values) RequestOpt {
	return func(req *http.Request) {
		req.URL.RawQuery = q.Encode()
	}
}

// WithRetryUntil will retry the request until the provided function returns true. Times out after
// `timeout`, which will then fail the test.
func WithRetryUntil(timeout time.Duration, untilFn func(res *http.Response) bool) RequestOpt {
	return func(req *http.Request) {
		until := req.Context().Value(CtxKeyWithRetryUntil).(*retryUntilParams)
		until.timeout = timeout
		until.untilFn = untilFn
	}
}

// MustDoFunc is the same as DoFunc but fails the test if the returned HTTP response code is not 2xx.
func (c *CSAPI) MustDoFunc(t *testing.T, method string, paths []string, opts ...RequestOpt) *http.Response {
	t.Helper()
	res := c.DoFunc(t, method, paths, opts...)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		body, _ := ioutil.ReadAll(res.Body)
		t.Fatalf("CSAPI.MustDoFunc response return non-2xx code: %s - body: %s", res.Status, string(body))
	}
	return res
}

func (c *CSAPI) SendToDevice(t *testing.T, eventType, userID, deviceID string, content map[string]interface{}) {
	c.txnID++
	c.MustDoFunc(t, "PUT", []string{"_matrix", "client", "v3", "sendToDevice", eventType, strconv.Itoa(c.txnID)}, WithJSONBody(t, map[string]interface{}{
		"messages": map[string]interface{}{
			userID: map[string]interface{}{
				deviceID: content,
			},
		},
	}))
}

// SlidingSync performs a single sliding sync request
func (c *CSAPI) SlidingSync(t *testing.T, data sync3.Request, opts ...RequestOpt) (resBody *sync3.Response) {
	t.Helper()
	if len(opts) == 0 {
		opts = append(opts, WithQueries(url.Values{
			"timeout": []string{"500"},
		}))
	}
	opts = append(opts, WithJSONBody(t, data))
	res := c.MustDoFunc(t, "POST", []string{"_matrix", "client", "unstable", "org.matrix.msc3575", "sync"}, opts...)
	body := ParseJSON(t, res)
	if err := json.Unmarshal(body, &resBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	return
}

func (c *CSAPI) SlidingSyncUntilEventID(t *testing.T, pos string, roomID string, eventID string) (res *sync3.Response) {
	t.Helper()
	return c.SlidingSyncUntilEvent(t, pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 10,
			},
		},
	}, roomID, Event{ID: eventID})
}

func (c *CSAPI) SlidingSyncUntilMembership(t *testing.T, pos string, roomID string, target *CSAPI, membership string) (res *sync3.Response) {
	t.Helper()
	content := map[string]interface{}{
		"membership": membership,
	}

	if membership == "join" || membership == "invite" {
		content["displayname"] = target.Localpart
	}

	if membership == "invite" && c == target {
		return c.SlidingSyncUntil(t, pos, sync3.Request{
			RoomSubscriptions: map[string]sync3.RoomSubscription{
				roomID: {
					TimelineLimit: 10,
				},
			},
		}, func(r *sync3.Response) error {
			room, exists := r.Rooms[roomID]
			if !exists {
				return fmt.Errorf("room %v does not exist", roomID)
			}
			if len(room.InviteState) > 0 {
				return nil
			}
			return fmt.Errorf("found room %v but it has no invite_state: %+v", roomID, room)
		})
	}

	return c.SlidingSyncUntilEvent(t, pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 10,
			},
		},
	}, roomID, Event{
		Type:     "m.room.member",
		StateKey: &target.UserID,
		Content:  content,
	})
}

func (c *CSAPI) SlidingSyncUntil(t *testing.T, pos string, data sync3.Request, check func(*sync3.Response) error) (res *sync3.Response) {
	t.Helper()
	start := time.Now()
	for time.Since(start) < 10*time.Second {
		qps := url.Values{
			"timeout": []string{"500"},
		}
		if pos != "" {
			qps["pos"] = []string{pos}
		}
		opts := []RequestOpt{
			WithQueries(qps),
		}
		res := c.SlidingSync(t, data, opts...)
		pos = res.Pos
		err := check(res)
		if err == nil {
			return res
		} else {
			t.Logf("SlidingSyncUntil: tested response but it failed with: %v", err)
		}
	}
	t.Fatalf("SlidingSyncUntil: timed out")
	return nil
}

// SlidingSyncUntilEvent repeatedly calls sliding sync until the given Event is seen. The seen event can be matched
// on the event ID, type and state_key, etc. A zero Event always passes.
func (c *CSAPI) SlidingSyncUntilEvent(t *testing.T, pos string, data sync3.Request, roomID string, want Event) (res *sync3.Response) {
	return c.SlidingSyncUntil(t, pos, data, func(r *sync3.Response) error {
		room, ok := r.Rooms[roomID]
		if !ok {
			return fmt.Errorf("missing room %s", roomID)
		}
		for _, got := range room.Timeline {
			if err := eventsEqual([]Event{want}, []json.RawMessage{got}); err == nil {
				return nil
			}
		}
		return fmt.Errorf("found room %s but missing event", roomID)
	})
}

// DoFunc performs an arbitrary HTTP request to the server. This function supports RequestOpts to set
// extra information on the request such as an HTTP request body, query parameters and content-type.
// See all functions in this package starting with `With...`.
//
// Fails the test if an HTTP request could not be made or if there was a network error talking to the
// server. To do assertions on the HTTP response, see the `must` package. For example:
//
//	must.MatchResponse(t, res, match.HTTPResponse{
//		StatusCode: 400,
//		JSON: []match.JSON{
//			match.JSONKeyEqual("errcode", "M_INVALID_USERNAME"),
//		},
//	})
func (c *CSAPI) DoFunc(t *testing.T, method string, paths []string, opts ...RequestOpt) *http.Response {
	t.Helper()
	isSlidingSync := false
	for i := range paths {
		if paths[i] == "org.matrix.msc3575" {
			isSlidingSync = true
		}
		paths[i] = url.PathEscape(paths[i])
	}
	var reqURL string
	if isSlidingSync {
		reqURL = proxyBaseURL + "/" + strings.Join(paths, "/")
	} else {
		reqURL = c.BaseURL + "/" + strings.Join(paths, "/")
	}
	req, err := http.NewRequest(method, reqURL, nil)
	if err != nil {
		t.Fatalf("CSAPI.DoFunc failed to create http.NewRequest: %s", err)
	}
	// set defaults before RequestOpts
	if c.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	}
	retryUntil := &retryUntilParams{}
	ctx := context.WithValue(req.Context(), CtxKeyWithRetryUntil, retryUntil)
	req = req.WithContext(ctx)

	// set functional options
	for _, o := range opts {
		o(req)
	}
	// set defaults after RequestOpts
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// debug log the request
	if c.Debug {
		t.Logf("Making %s request to %s (%s)", method, req.URL, c.AccessToken)
		contentType := req.Header.Get("Content-Type")
		if contentType == "application/json" || strings.HasPrefix(contentType, "text/") {
			if req.Body != nil {
				body, _ := ioutil.ReadAll(req.Body)
				t.Logf("Request body: %s", string(body))
				req.Body = ioutil.NopCloser(bytes.NewBuffer(body))
			}
		} else {
			t.Logf("Request body: <binary:%s>", contentType)
		}
	}
	now := time.Now()
	for {
		// Perform the HTTP request
		res, err := c.Client.Do(req)
		if err != nil {
			t.Fatalf("CSAPI.DoFunc response returned error: %s", err)
		}
		// debug log the response
		if c.Debug && res != nil {
			var dump []byte
			dump, err = httputil.DumpResponse(res, true)
			if err != nil {
				t.Fatalf("CSAPI.DoFunc failed to dump response body: %s", err)
			}
			t.Logf("%s", string(dump))
		}
		if retryUntil == nil || retryUntil.timeout == 0 {
			return res // don't retry
		}

		// check the condition, make a copy of the response body first in case the check consumes it
		var resBody []byte
		if res.Body != nil {
			resBody, err = ioutil.ReadAll(res.Body)
			if err != nil {
				t.Fatalf("CSAPI.DoFunc failed to read response body for RetryUntil check: %s", err)
			}
			res.Body = io.NopCloser(bytes.NewBuffer(resBody))
		}
		if retryUntil.untilFn(res) {
			// remake the response and return
			res.Body = io.NopCloser(bytes.NewBuffer(resBody))
			return res
		}
		// condition not satisfied, do we timeout yet?
		if time.Since(now) > retryUntil.timeout {
			t.Fatalf("CSAPI.DoFunc RetryUntil: %v %v timed out after %v", method, req.URL, retryUntil.timeout)
		}
		t.Logf("CSAPI.DoFunc RetryUntil: %v %v response condition not yet met, retrying", method, req.URL)
		// small sleep to avoid tight-looping
		time.Sleep(100 * time.Millisecond)
	}
}

// NewLoggedClient returns an http.Client which logs requests/responses
func NewLoggedClient(t *testing.T, hsName string, cli *http.Client) *http.Client {
	t.Helper()
	if cli == nil {
		cli = &http.Client{
			Timeout: 30 * time.Second,
		}
	}
	transport := cli.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	cli.Transport = &loggedRoundTripper{t, hsName, transport}
	return cli
}

type loggedRoundTripper struct {
	t      *testing.T
	hsName string
	wrap   http.RoundTripper
}

func (t *loggedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	res, err := t.wrap.RoundTrip(req)
	if err != nil {
		t.t.Logf("%s %s%s => error: %s (%s)", req.Method, t.hsName, req.URL.Path, err, time.Since(start))
	} else {
		t.t.Logf("%s %s%s => %s (%s)", req.Method, t.hsName, req.URL.Path, res.Status, time.Since(start))
	}
	return res, err
}

// GetJSONFieldStr extracts a value from a byte-encoded JSON body given a search key
func GetJSONFieldStr(t *testing.T, body []byte, wantKey string) string {
	t.Helper()
	res := gjson.GetBytes(body, wantKey)
	if !res.Exists() {
		t.Fatalf("JSONFieldStr: key '%s' missing from %s", wantKey, string(body))
	}
	if res.Str == "" {
		t.Fatalf("JSONFieldStr: key '%s' is not a string, body: %s", wantKey, string(body))
	}
	return res.Str
}

func GetJSONFieldStringArray(t *testing.T, body []byte, wantKey string) []string {
	t.Helper()

	res := gjson.GetBytes(body, wantKey)

	if !res.Exists() {
		t.Fatalf("JSONFieldStr: key '%s' missing from %s", wantKey, string(body))
	}

	arrLength := len(res.Array())
	arr := make([]string, arrLength)
	i := 0
	res.ForEach(func(key, value gjson.Result) bool {
		arr[i] = value.Str

		// Keep iterating
		i++
		return true
	})

	return arr
}

// ParseJSON parses a JSON-encoded HTTP Response body into a byte slice
func ParseJSON(t *testing.T, res *http.Response) []byte {
	t.Helper()
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("MustParseJSON: reading HTTP response body returned %s", err)
	}
	if !gjson.ValidBytes(body) {
		t.Fatalf("MustParseJSON: Response is not valid JSON")
	}
	return body
}

// GjsonEscape escapes . and * from the input so it can be used with gjson.Get
func GjsonEscape(in string) string {
	in = strings.ReplaceAll(in, ".", `\.`)
	in = strings.ReplaceAll(in, "*", `\*`)
	return in
}

// Check that the timeline for `roomID` has an event which passes the check function.
func SyncTimelineHas(roomID string, check func(gjson.Result) bool) SyncCheckOpt {
	return func(clientUserID string, topLevelSyncJSON gjson.Result) error {
		err := loopArray(
			topLevelSyncJSON, "rooms.join."+GjsonEscape(roomID)+".timeline.events", check,
		)
		if err == nil {
			return nil
		}
		return fmt.Errorf("SyncTimelineHas(%s): %s", roomID, err)
	}
}

// Check that the timeline for `roomID` has an event which matches the event ID.
func SyncTimelineHasEventID(roomID string, eventID string) SyncCheckOpt {
	return SyncTimelineHas(roomID, func(ev gjson.Result) bool {
		return ev.Get("event_id").Str == eventID
	})
}

// Checks that `userID` gets invited to `roomID`.
//
// This checks different parts of the /sync response depending on the client making the request.
// If the client is also the person being invited to the room then the 'invite' block will be inspected.
// If the client is different to the person being invited then the 'join' block will be inspected.
func SyncInvitedTo(userID, roomID string) SyncCheckOpt {
	return func(clientUserID string, topLevelSyncJSON gjson.Result) error {
		// two forms which depend on what the client user is:
		// - passively viewing an invite for a room you're joined to (timeline events)
		// - actively being invited to a room.
		if clientUserID == userID {
			// active
			err := loopArray(
				topLevelSyncJSON, "rooms.invite."+GjsonEscape(roomID)+".invite_state.events",
				func(ev gjson.Result) bool {
					return ev.Get("type").Str == "m.room.member" && ev.Get("state_key").Str == userID && ev.Get("content.membership").Str == "invite"
				},
			)
			if err != nil {
				return fmt.Errorf("SyncInvitedTo(%s): %s", roomID, err)
			}
			return nil
		}
		// passive
		return SyncTimelineHas(roomID, func(ev gjson.Result) bool {
			return ev.Get("type").Str == "m.room.member" && ev.Get("state_key").Str == userID && ev.Get("content.membership").Str == "invite"
		})(clientUserID, topLevelSyncJSON)
	}
}

// Check that `userID` gets joined to `roomID` by inspecting the join timeline for a membership event
func SyncJoinedTo(userID, roomID string) SyncCheckOpt {
	return func(clientUserID string, topLevelSyncJSON gjson.Result) error {
		// awkward wrapping to get the error message correct at the start :/
		err := SyncTimelineHas(roomID, func(ev gjson.Result) bool {
			return ev.Get("type").Str == "m.room.member" && ev.Get("state_key").Str == userID && ev.Get("content.membership").Str == "join"
		})(clientUserID, topLevelSyncJSON)
		if err == nil {
			return nil
		}
		return fmt.Errorf("SyncJoinedTo(%s,%s): %s", userID, roomID, err)
	}
}

// Check that `userID` is leaving `roomID` by inspecting the timeline for a membership event, or witnessing `roomID` in `rooms.leave`
// Note: This will not work properly with initial syncs, see https://github.com/matrix-org/matrix-doc/issues/3537
func SyncLeftFrom(userID, roomID string) SyncCheckOpt {
	return func(clientUserID string, topLevelSyncJSON gjson.Result) error {
		// two forms which depend on what the client user is:
		// - passively viewing a membership for a room you're joined in
		// - actively leaving the room
		if clientUserID == userID {
			// active
			events := topLevelSyncJSON.Get("rooms.leave." + GjsonEscape(roomID))
			if !events.Exists() {
				return fmt.Errorf("no leave section for room %s", roomID)
			} else {
				return nil
			}
		}
		// passive
		return SyncTimelineHas(roomID, func(ev gjson.Result) bool {
			return ev.Get("type").Str == "m.room.member" && ev.Get("state_key").Str == userID && ev.Get("content.membership").Str == "leave"
		})(clientUserID, topLevelSyncJSON)
	}
}

// Calls the `check` function for each global account data event, and returns with success if the
// `check` function returns true for at least one event.
func SyncGlobalAccountDataHas(check func(gjson.Result) bool) SyncCheckOpt {
	return func(clientUserID string, topLevelSyncJSON gjson.Result) error {
		return loopArray(topLevelSyncJSON, "account_data.events", check)
	}
}

func loopArray(object gjson.Result, key string, check func(gjson.Result) bool) error {
	array := object.Get(key)
	if !array.Exists() {
		return fmt.Errorf("Key %s does not exist", key)
	}
	if !array.IsArray() {
		return fmt.Errorf("Key %s exists but it isn't an array", key)
	}
	goArray := array.Array()
	for _, ev := range goArray {
		if check(ev) {
			return nil
		}
	}
	return fmt.Errorf("check function did not pass while iterating over %d elements: %v", len(goArray), array.Raw)
}

// Splits an MXC URI into its origin and media ID parts
func SplitMxc(mxcUri string) (string, string) {
	mxcParts := strings.Split(strings.TrimPrefix(mxcUri, "mxc://"), "/")
	origin := mxcParts[0]
	mediaId := strings.Join(mxcParts[1:], "/")

	return origin, mediaId
}
