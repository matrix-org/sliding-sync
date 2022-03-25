package sync2

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/tidwall/gjson"
)

const AccountDataGlobalRoom = ""

type Client interface {
	WhoAmI(authHeader string) (string, error)
	DoSyncV2(authHeader, since string, isFirst bool) (*SyncResponse, int, error)
}

// HTTPClient represents a Sync v2 Client.
// One client can be shared among many users.
type HTTPClient struct {
	Client            *http.Client
	DestinationServer string
}

func (v *HTTPClient) WhoAmI(authHeader string) (string, error) {
	req, err := http.NewRequest("GET", v.DestinationServer+"/_matrix/client/r0/account/whoami", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "sync-v3-proxy")
	req.Header.Set("Authorization", authHeader)
	res, err := v.Client.Do(req)
	if err != nil {
		return "", err
	}
	if res.StatusCode != 200 {
		return "", fmt.Errorf("/whoami returned HTTP %d", res.StatusCode)
	}
	defer res.Body.Close()
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	return gjson.GetBytes(body, "user_id").Str, nil
}

// DoSyncV2 performs a sync v2 request. Returns the sync response and the response status code
// or an error. Set isFirst=true on the first sync to force a timeout=0 sync to ensure snapiness.
func (v *HTTPClient) DoSyncV2(authHeader, since string, isFirst bool) (*SyncResponse, int, error) {
	qps := "?"
	if isFirst {
		qps += "timeout=0"
	} else {
		qps += "timeout=30000"
	}
	if since != "" {
		qps += "&since=" + since
	}
	req, err := http.NewRequest(
		"GET", v.DestinationServer+"/_matrix/client/r0/sync"+qps, nil,
	)
	req.Header.Set("User-Agent", "sync-v3-proxy")
	req.Header.Set("Authorization", authHeader)
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
	DeviceListsOTKCount map[string]int `json:"device_one_time_keys_count,omitempty"`
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
