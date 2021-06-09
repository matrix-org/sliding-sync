package sync2

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/matrix-org/gomatrixserverlib"
)

// Client represents a Sync v2 Client.
// One client can be shared among many users.
type Client struct {
	Client            *http.Client
	DestinationServer string
}

// DoSyncV2 performs a sync v2 request. Returns the sync response and the response status code
// or an error
func (v *Client) DoSyncV2(authHeader, since string) (*SyncResponse, int, error) {
	qps := "?timeout=30000"
	if since != "" {
		qps += "&since=" + since
	}
	req, err := http.NewRequest(
		"GET", v.DestinationServer+"/_matrix/client/r0/sync"+qps, nil,
	)
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
	NextBatch   string `json:"next_batch"`
	AccountData struct {
		Events []gomatrixserverlib.ClientEvent `json:"events,omitempty"`
	} `json:"account_data"`
	Presence struct {
		Events []gomatrixserverlib.ClientEvent `json:"events,omitempty"`
	} `json:"presence"`
	Rooms struct {
		Join   map[string]SyncV2JoinResponse   `json:"join"`
		Invite map[string]SyncV2InviteResponse `json:"invite"`
		Leave  map[string]SyncV2LeaveResponse  `json:"leave"`
	} `json:"rooms"`
	ToDevice struct {
		Events []gomatrixserverlib.SendToDeviceEvent `json:"events"`
	} `json:"to_device"`
	DeviceLists struct {
		Changed []string `json:"changed,omitempty"`
		Left    []string `json:"left,omitempty"`
	} `json:"device_lists"`
	DeviceListsOTKCount map[string]int `json:"device_one_time_keys_count,omitempty"`
}

// JoinResponse represents a /sync response for a room which is under the 'join' or 'peek' key.
type SyncV2JoinResponse struct {
	State struct {
		Events []json.RawMessage `json:"events"`
	} `json:"state"`
	Timeline struct {
		Events    []json.RawMessage `json:"events"`
		Limited   bool              `json:"limited"`
		PrevBatch string            `json:"prev_batch,omitempty"`
	} `json:"timeline"`
	Ephemeral struct {
		Events []json.RawMessage `json:"events"`
	} `json:"ephemeral"`
	AccountData struct {
		Events []json.RawMessage `json:"events"`
	} `json:"account_data"`
}

// InviteResponse represents a /sync response for a room which is under the 'invite' key.
type SyncV2InviteResponse struct {
	InviteState struct {
		Events []json.RawMessage `json:"events"`
	} `json:"invite_state"`
}

// LeaveResponse represents a /sync response for a room which is under the 'leave' key.
type SyncV2LeaveResponse struct {
	State struct {
		Events []gomatrixserverlib.ClientEvent `json:"events"`
	} `json:"state"`
	Timeline struct {
		Events    []gomatrixserverlib.ClientEvent `json:"events"`
		Limited   bool                            `json:"limited"`
		PrevBatch string                          `json:"prev_batch,omitempty"`
	} `json:"timeline"`
}
