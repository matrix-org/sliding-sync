package syncv3_test

import (
	"fmt"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
	"io/ioutil"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncContinuesAfterTokenRefresh(t *testing.T) {
	alice := registerNewUserRefreshingTokens(t)
	bob := registerNewUser(t)

	t.Log("Alice creates a room.")
	roomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})

	t.Log("Bob joins the room.")
	bob.JoinRoom(t, roomID, nil)

	t.Log("Alice makes an initial sliding sync")
	res := alice.SlidingSync(
		t,
		sync3.Request{
			Lists: map[string]sync3.RequestList{
				"rooms": {
					Ranges: sync3.SliceRanges{[2]int64{0, 10}},
					RoomSubscription: sync3.RoomSubscription{
						TimelineLimit: 20,
					},
				},
			},
		},
	)
	t.Log("Alice should see Bob's membership")
	m.MatchResponse(
		t,
		res,
		m.MatchRoomSubscription(
			roomID,
			MatchRoomTimelineMostRecent(1, []Event{{
				Type:     "m.room.member",
				Sender:   bob.UserID,
				StateKey: &bob.UserID,
			}}),
		),
	)

	t.Log("Alice refreshes her access token.")
	oldToken := alice.AccessToken
	alice.RefreshAccessToken(t)
	newToken := alice.AccessToken

	t.Log("Alice makes a request using the new token.")
	alice.MustDoFunc(t, "GET", []string{"_matrix", "client", "v3", "account", "whoami"})

	t.Log("Check the old token has now expired.")
	// TODO: this fails---it seems that only the _refresh_ token expires after its use,
	// and not its corresponding access token. Synapse expires refreshing tokens after
	// 5 minutes. So I could sleep for 300 sec... but that's pants.
	alice.AccessToken = oldToken
	whoami := alice.DoFunc(t, "GET", []string{"_matrix", "client", "v3", "account", "whoami"})
	if whoami.StatusCode != 401 {
		t.Fatalf("Expected old token to receive 401 from /whoami, but got %d", whoami.StatusCode)
	}

	t.Log("Bob sends an event.")
	msgID := bob.SendEventUnsynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello, world!",
		},
	})

	t.Log("Alice uses her new token to do an incremental sync. She eventually sees Bob's message.")
	alice.AccessToken = newToken
	alice.SlidingSyncUntilEventID(t, res.Pos, roomID, msgID)
}

func registerNewUserRefreshingTokens(t *testing.T) *CSAPI {
	httpClient := NewLoggedClient(t, "localhost", nil)
	client := &CSAPI{
		Client:           httpClient,
		BaseURL:          homeserverBaseURL,
		SyncUntilTimeout: 3 * time.Second,
	}
	localpart := fmt.Sprintf("user-%d-%d", time.Now().Unix(), atomic.AddUint64(&userCounter, 1))

	reqBody := map[string]interface{}{
		"auth": map[string]string{
			"type": "m.login.dummy",
		},
		"username":      localpart,
		"password":      "password",
		"refresh_token": true,
	}
	res := client.MustDoFunc(
		t,
		"POST",
		[]string{"_matrix", "client", "v3", "register"},
		WithJSONBody(t, reqBody),
	)

	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("unable to read response body: %v", err)
	}

	client.UserID = gjson.GetBytes(body, "user_id").Str
	client.AccessToken = gjson.GetBytes(body, "access_token").Str
	client.RefreshToken = gjson.GetBytes(body, "refresh_token").Str
	client.DeviceID = gjson.GetBytes(body, "device_id").Str
	client.Localpart = strings.Split(client.UserID, ":")[0][1:]
	return client
}
