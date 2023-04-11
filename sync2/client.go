package sync2

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/tidwall/gjson"
)

const AccountDataGlobalRoom = ""

var ProxyVersion = ""
var HTTP401 error = fmt.Errorf("HTTP 401")

type Client interface {
	// WhoAmI asks the homeserver to lookup the access token using the CSAPI /whoami
	// endpoint. The response must contain a device ID (meaning that we assume the
	// homeserver supports Matrix >= 1.1.)
	WhoAmI(accessToken string) (userID, deviceID string, err error)
	DoSyncV2(ctx context.Context, accessToken, since string, isFirst bool, toDeviceOnly bool) (*SyncResponse, int, error)
}

// HTTPClient represents a Sync v2 Client.
// One client can be shared among many users.
type HTTPClient struct {
	Client            *http.Client
	DestinationServer string
}

// Return sync2.HTTP401 if this request returns 401
func (v *HTTPClient) WhoAmI(accessToken string) (string, string, error) {
	req, err := http.NewRequest("GET", v.DestinationServer+"/_matrix/client/r0/account/whoami", nil)
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
	body, err := ioutil.ReadAll(res.Body)
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
	req, err := http.NewRequest("GET", syncURL, nil)
	req.Header.Set("User-Agent", "sync-v3-proxy-"+ProxyVersion)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if err != nil {
		return nil, 0, fmt.Errorf("DoSyncV2: NewRequest failed: %w", err)
	}
	res, err := v.Client.Do(req)
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
	if isFirst { // first time syncing in this process
		qps += "timeout=0"
	} else {
		qps += "timeout=30000"
	}
	if since != "" {
		qps += "&since=" + since
	}

	timelineLimitOne := since == ""
	if timelineLimitOne || toDeviceOnly {
		room := map[string]interface{}{}
		if timelineLimitOne {
			room["timeline"] = map[string]interface{}{
				"limit": 1,
			}
		}
		if toDeviceOnly {
			// no rooms match this filter, so we get everything but room data
			room["rooms"] = []string{}
		}
		filter := map[string]interface{}{
			"room": room,
		}
		filterJSON, _ := json.Marshal(filter)
		qps += "&filter=" + url.QueryEscape(string(filterJSON))
	}
	return v.DestinationServer + "/_matrix/client/r0/sync" + qps
}

type SyncResponse struct {
	NextBatch   string         `json:"next_batch"`
	AccountData EventsResponse `json:"account_data"`
	Presence    struct {
		Events []gomatrixserverlib.ClientEvent `json:"events,omitempty"`
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
