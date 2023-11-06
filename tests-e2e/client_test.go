package syncv3_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/complement/must"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/tidwall/gjson"
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
	Unsigned map[string]interface{} `json:"unsigned"`
}

func WithPos(pos string) client.RequestOpt {
	return client.WithQueries(url.Values{
		"pos":     []string{pos},
		"timeout": []string{"500"}, // 0.5s
	})
}

type CSAPI struct {
	*client.CSAPI
	Localpart string
	Domain    string
	AvatarURL string
}

// TODO: put this in Complement at some point? Check usage.
func (c *CSAPI) Scrollback(t *testing.T, roomID, prevBatch string, limit int) gjson.Result {
	t.Helper()
	res := c.MustDo(t, "GET", []string{
		"_matrix", "client", "v3", "rooms", roomID, "messages",
	}, client.WithQueries(url.Values{
		"dir":   []string{"b"},
		"from":  []string{prevBatch},
		"limit": []string{strconv.Itoa(limit)},
	}))
	return must.ParseJSON(t, res.Body)
}

// SlidingSync performs a single sliding sync request. Fails on non 2xx
func (c *CSAPI) SlidingSync(t *testing.T, data sync3.Request, opts ...client.RequestOpt) (resBody *sync3.Response) {
	t.Helper()
	res := c.DoSlidingSync(t, data, opts...)
	body := client.ParseJSON(t, res)
	if err := json.Unmarshal(body, &resBody); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	return
}

// DoSlidingSync is the same as SlidingSync but returns the raw HTTP response. Succeeds on any status code.
func (c *CSAPI) DoSlidingSync(t *testing.T, data sync3.Request, opts ...client.RequestOpt) (res *http.Response) {
	t.Helper()
	if len(opts) == 0 {
		opts = append(opts, client.WithQueries(url.Values{
			"timeout": []string{"500"},
		}))
	}
	opts = append(opts, client.WithJSONBody(t, data))
	// copy the CSAPI struct and tweak the base URL so we talk to the proxy not synapse
	csapi := *c.CSAPI
	csapi.BaseURL = proxyBaseURL
	return csapi.Do(t, "POST", []string{"_matrix", "client", "unstable", "org.matrix.msc3575", "sync"}, opts...)
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
		content["displayname"] = target.CSAPI.UserID // FIXME
	}

	if membership == "invite" && c == target {
		return c.SlidingSyncUntil(t, pos, sync3.Request{
			RoomSubscriptions: map[string]sync3.RoomSubscription{
				roomID: {
					TimelineLimit: 10,
					Heroes:        &boolTrue,
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

	return c.SlidingSyncUntil(t, pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 10,
				Heroes:        &boolTrue,
			},
		},
	}, func(r *sync3.Response) error {
		room, ok := r.Rooms[roomID]
		if !ok {
			return fmt.Errorf("missing room %s", roomID)
		}
		for _, got := range room.Timeline {
			wantEvent := Event{
				Type:     "m.room.member",
				StateKey: &target.UserID,
			}
			if err := eventsEqual([]Event{wantEvent}, []json.RawMessage{got}); err == nil {
				gotMembership := gjson.GetBytes(got, "content.membership")
				if gotMembership.Exists() && gotMembership.Type == gjson.String && gotMembership.Str == membership {
					return nil
				}
			} else {
				t.Log(err)
			}
		}
		return fmt.Errorf("found room %s but missing event", roomID)
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
		opts := []client.RequestOpt{
			client.WithQueries(qps),
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
	res := c.MustDo(t, "POST", []string{"_matrix", "client", "v3", "login"}, client.WithJSONBody(t, reqBody))

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

// Use an empty string to remove a custom displayname.
func (c *CSAPI) SetDisplayname(t *testing.T, name string) {
	t.Helper()
	reqBody := map[string]any{}
	if name != "" {
		reqBody["displayname"] = name
	}
	c.MustDo(t, "PUT", []string{"_matrix", "client", "v3", "profile", c.UserID, "displayname"}, client.WithJSONBody(t, reqBody))
}

// Use an empty string to remove your avatar.
func (c *CSAPI) SetAvatar(t *testing.T, avatarURL string) {
	t.Helper()
	reqBody := map[string]interface{}{
		"avatar_url": avatarURL,
	}
	c.MustDo(t, "PUT", []string{"_matrix", "client", "v3", "profile", c.UserID, "avatar_url"}, client.WithJSONBody(t, reqBody))
	c.AvatarURL = avatarURL
}

func (c *CSAPI) SendReceipt(t *testing.T, roomID, eventID, receiptType string) *http.Response {
	return c.MustDo(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "read_markers"}, client.WithJSONBody(t, map[string]interface{}{
		receiptType: eventID,
	}))
}

func NewEncryptionEvent() b.Event {
	return b.Event{
		Type:     "m.room.encryption",
		StateKey: ptr(""),
		Content: map[string]interface{}{
			"algorithm":            "m.megolm.v1.aes-sha2",
			"rotation_period_ms":   604800000,
			"rotation_period_msgs": 100,
		},
	}
}
