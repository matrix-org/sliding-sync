package sync2

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/matrix-org/sliding-sync/internal"
	"io"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/tidwall/gjson"
)

const AccountDataGlobalRoom = ""

var ProxyVersion = ""
var HTTP401 error = fmt.Errorf("HTTP 401")

type Client interface {
	// Versions fetches and parses the list of Matrix versions that the homeserver
	// advertises itself as supporting.
	Versions(ctx context.Context) (version []string, err error)
	// WhoAmI asks the homeserver to lookup the access token using the CSAPI /whoami
	// endpoint. The response must contain a device ID (meaning that we assume the
	// homeserver supports Matrix >= 1.1.)
	WhoAmI(ctx context.Context, accessToken string) (userID, deviceID string, err error)
	DoSyncV2(ctx context.Context, accessToken, since string, isFirst bool, toDeviceOnly bool) (*SyncResponse, int, error)
}

// HTTPClient represents a Sync v2 Client.
// One client can be shared among many users.
type HTTPClient struct {
	Client            *http.Client
	LongTimeoutClient *http.Client
	DestinationServer string
}

func NewHTTPClient(shortTimeout, longTimeout time.Duration, destHomeServer string) *HTTPClient {
	return &HTTPClient{
		LongTimeoutClient: newClient(longTimeout, destHomeServer),
		Client:            newClient(shortTimeout, destHomeServer),
		DestinationServer: internal.GetBaseURL(destHomeServer),
	}
}

func newClient(timeout time.Duration, destHomeServer string) *http.Client {
	transport := http.DefaultTransport
	if internal.IsUnixSocket(destHomeServer) {
		transport = internal.UnixTransport(destHomeServer)
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: otelhttp.NewTransport(transport),
	}
}

func (v *HTTPClient) Versions(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", v.DestinationServer+"/_matrix/client/versions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "sync-v3-proxy-"+ProxyVersion)
	res, err := v.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/versions returned HTTP %d", res.StatusCode)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var parsedRes struct {
		Result []string `json:"versions"`
	}
	err = json.Unmarshal(body, &parsedRes)
	if err != nil {
		return nil, fmt.Errorf("could not parse /versions response: %w", err)
	}
	return parsedRes.Result, nil
}

// Return sync2.HTTP401 if this request returns 401
func (v *HTTPClient) WhoAmI(ctx context.Context, accessToken string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", v.DestinationServer+"/_matrix/client/r0/account/whoami", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "sync-v3-proxy-"+ProxyVersion)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	res, err := v.Client.Do(req)
	if err != nil {
		return "", "", err
	}
	if res.StatusCode != 200 {
		if res.StatusCode == 401 {
			return "", "", HTTP401
		}
		return "", "", fmt.Errorf("/whoami returned HTTP %d", res.StatusCode)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", "", err
	}
	response := gjson.ParseBytes(body)
	return response.Get("user_id").Str, response.Get("device_id").Str, nil
}

// DoSyncV2 performs a sync v2 request. Returns the sync response and the response status code
// or an error. Set isFirst=true on the first sync to force a timeout=0 sync to ensure snapiness.
func (v *HTTPClient) DoSyncV2(ctx context.Context, accessToken, since string, isFirst, toDeviceOnly bool) (*SyncResponse, int, error) {
	syncURL := v.createSyncURL(since, isFirst, toDeviceOnly)
	req, err := http.NewRequestWithContext(ctx, "GET", syncURL, nil)
	req.Header.Set("User-Agent", "sync-v3-proxy-"+ProxyVersion)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if err != nil {
		return nil, 0, fmt.Errorf("DoSyncV2: NewRequest failed: %w", err)
	}
	var res *http.Response
	if isFirst {
		res, err = v.LongTimeoutClient.Do(req)
	} else {
		res, err = v.Client.Do(req)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("DoSyncV2: request failed: %w", err)
	}
	switch res.StatusCode {
	case 200:
		var svr SyncResponse
		if err := json.NewDecoder(res.Body).Decode(&svr); err != nil {
			return nil, 0, fmt.Errorf("DoSyncV2: response body decode JSON failed: %w", err)
		}
		return &svr, 200, nil
	default:
		return nil, res.StatusCode, fmt.Errorf("DoSyncV2: response returned %s", res.Status)
	}
}

func (v *HTTPClient) createSyncURL(since string, isFirst, toDeviceOnly bool) string {
	qps := "?"
	if isFirst { // first time polling for v2-sync in this process
		qps += "timeout=0"
	} else {
		qps += "timeout=30000"
	}
	if since != "" {
		qps += "&since=" + since
	}

	// Set presence to offline, this potentially reduces CPU load on upstream homeservers
	qps += "&set_presence=offline"

	// To reduce the likelihood of a gappy v2 sync, ask for a large timeline by default.
	// Synapse's default is 10; 50 is the maximum allowed, by my reading of
	// https://github.com/matrix-org/synapse/blob/89a71e73905ffa1c97ae8be27d521cd2ef3f3a0c/synapse/handlers/sync.py#L576-L577
	// NB: this is a stopgap to reduce the likelihood of hitting
	// https://github.com/matrix-org/sliding-sync/issues/18
	timelineLimit := 50
	if since == "" {
		// First time the poller has sync v2-ed for this user
		timelineLimit = 1
	}
	room := map[string]interface{}{}
	room["timeline"] = map[string]interface{}{"limit": timelineLimit}

	if toDeviceOnly {
		// no rooms match this filter, so we get everything but room data
		room["rooms"] = []string{}
	}
	filter := map[string]interface{}{
		"room": room,
		// filter out all presence events (remove this once/if the proxy handles presence)
		"presence": map[string]interface{}{"not_types": []string{"*"}},
	}
	filterJSON, _ := json.Marshal(filter)
	qps += "&filter=" + url.QueryEscape(string(filterJSON))

	return v.DestinationServer + "/_matrix/client/r0/sync" + qps
}

type SyncResponse struct {
	NextBatch   string         `json:"next_batch"`
	AccountData EventsResponse `json:"account_data"`
	Presence    struct {
		Events []json.RawMessage `json:"events,omitempty"`
	} `json:"presence"`
	Rooms       SyncRoomsResponse `json:"rooms"`
	ToDevice    EventsResponse    `json:"to_device"`
	DeviceLists struct {
		Changed []string `json:"changed,omitempty"`
		Left    []string `json:"left,omitempty"`
	} `json:"device_lists"`
	DeviceListsOTKCount          map[string]int `json:"device_one_time_keys_count,omitempty"`
	DeviceUnusedFallbackKeyTypes []string       `json:"device_unused_fallback_key_types,omitempty"`
}

type SyncRoomsResponse struct {
	Join   map[string]SyncV2JoinResponse   `json:"join"`
	Invite map[string]SyncV2InviteResponse `json:"invite"`
	Leave  map[string]SyncV2LeaveResponse  `json:"leave"`
}

// JoinResponse represents a /sync response for a room which is under the 'join' or 'peek' key.
type SyncV2JoinResponse struct {
	State               EventsResponse      `json:"state"`
	Timeline            TimelineResponse    `json:"timeline"`
	Ephemeral           EventsResponse      `json:"ephemeral"`
	AccountData         EventsResponse      `json:"account_data"`
	UnreadNotifications UnreadNotifications `json:"unread_notifications"`
}

type UnreadNotifications struct {
	HighlightCount    *int `json:"highlight_count,omitempty"`
	NotificationCount *int `json:"notification_count,omitempty"`
}

type TimelineResponse struct {
	Events    []json.RawMessage `json:"events"`
	Limited   bool              `json:"limited"`
	PrevBatch string            `json:"prev_batch,omitempty"`
}

type EventsResponse struct {
	Events []json.RawMessage `json:"events"`
}

// InviteResponse represents a /sync response for a room which is under the 'invite' key.
type SyncV2InviteResponse struct {
	InviteState EventsResponse `json:"invite_state"`
}

// LeaveResponse represents a /sync response for a room which is under the 'leave' key.
type SyncV2LeaveResponse struct {
	State struct {
		Events []json.RawMessage `json:"events"`
	} `json:"state"`
	Timeline struct {
		Events    []json.RawMessage `json:"events"`
		Limited   bool              `json:"limited"`
		PrevBatch string            `json:"prev_batch,omitempty"`
	} `json:"timeline"`
}
