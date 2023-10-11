package syncv3_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestInvalidTokenReturnsMUnknownTokenError(t *testing.T) {
	alice := registerNewUser(t)
	roomID := alice.MustCreateRoom(t, map[string]interface{}{})
	// normal sliding sync
	alice.SlidingSync(t, sync3.Request{
		ConnID: "A",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	})
	// invalidate the access token
	alice.MustDo(t, "POST", []string{"_matrix", "client", "v3", "logout"})
	// let the proxy realise the token is expired and tell downstream
	time.Sleep(time.Second)

	var invalidResponses []*http.Response
	// using the same token now returns a 401 with M_UNKNOWN_TOKEN
	httpRes := alice.Do(t, "POST", []string{"_matrix", "client", "unstable", "org.matrix.msc3575", "sync"}, client.WithQueries(url.Values{
		"timeout": []string{"500"},
	}), client.WithJSONBody(t, sync3.Request{
		ConnID: "A",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	}))
	invalidResponses = append(invalidResponses, httpRes)
	// using a bogus access token returns a 401 with M_UNKNOWN_TOKEN
	alice.AccessToken = "flibble_wibble"
	httpRes = alice.Do(t, "POST", []string{"_matrix", "client", "unstable", "org.matrix.msc3575", "sync"}, client.WithQueries(url.Values{
		"timeout": []string{"500"},
	}), client.WithJSONBody(t, sync3.Request{
		ConnID: "A",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	}))
	invalidResponses = append(invalidResponses, httpRes)

	for i, httpRes := range invalidResponses {
		body, err := io.ReadAll(httpRes.Body)
		if err != nil {
			t.Fatalf("[%d] failed to read response body: %v", i, err)
		}

		if httpRes.StatusCode != 401 {
			t.Errorf("[%d] got HTTP %v want 401: %v", i, httpRes.StatusCode, string(body))
		}
		var jsonError struct {
			Err     string `json:"error"`
			ErrCode string `json:"errcode"`
		}

		if err := json.Unmarshal(body, &jsonError); err != nil {
			t.Fatalf("[%d] failed to unmarshal error response into JSON: %v", i, string(body))
		}
		wantErrCode := "M_UNKNOWN_TOKEN"
		if jsonError.ErrCode != wantErrCode {
			t.Errorf("[%d] errcode: got %v want %v", i, jsonError.ErrCode, wantErrCode)
		}
	}
}

// Test that you can have multiple connections with the same device, and they work independently.
func TestMultipleConns(t *testing.T) {
	alice := registerNewUser(t)
	roomID := alice.MustCreateRoom(t, map[string]interface{}{})

	resA := alice.SlidingSync(t, sync3.Request{
		ConnID: "A",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 1,
			},
		},
	})
	resB := alice.SlidingSync(t, sync3.Request{
		ConnID: "B",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 2,
			},
		},
	})
	resC := alice.SlidingSync(t, sync3.Request{
		ConnID: "C",
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			roomID: {
				TimelineLimit: 0,
			},
		},
	})

	eventID := alice.SendEventSynced(t, roomID, b.Event{
		Type:     "m.room.name",
		StateKey: ptr(""),
		Content:  map[string]interface{}{"name": "pub"},
	})

	// all 3 conns should see the name event
	testCases := []struct {
		ConnID   string
		Res      *sync3.Response
		WriteRes func(res *sync3.Response)
	}{
		{
			ConnID: "A", Res: resA, WriteRes: func(res *sync3.Response) { resA = res },
		},
		{
			ConnID: "B", Res: resB, WriteRes: func(res *sync3.Response) { resB = res },
		},
		{
			ConnID: "C", Res: resC, WriteRes: func(res *sync3.Response) { resC = res },
		},
	}

	for _, tc := range testCases {
		t.Logf("Syncing as %v", tc.ConnID)
		r := alice.SlidingSyncUntil(t, tc.Res.Pos, sync3.Request{ConnID: tc.ConnID}, func(r *sync3.Response) error {
			room, ok := r.Rooms[roomID]
			if !ok {
				return fmt.Errorf("no room %v", roomID)
			}
			return MatchRoomTimelineMostRecent(1, []Event{{ID: eventID}})(room)
		})
		tc.WriteRes(r)
	}

	// change the requests in different ways, ensure they still work, without expiring.

	// Conn A: unsub from the room
	resA = alice.SlidingSync(t, sync3.Request{ConnID: "A", UnsubscribeRooms: []string{roomID}}, WithPos(resA.Pos))
	m.MatchResponse(t, resA, m.MatchRoomSubscriptionsStrict(nil)) // no rooms

	// Conn B: ask for the create event
	resB = alice.SlidingSync(t, sync3.Request{ConnID: "B", RoomSubscriptions: map[string]sync3.RoomSubscription{
		roomID: {
			RequiredState: [][2]string{{"m.room.create", ""}},
		},
	}}, WithPos(resB.Pos))
	m.MatchResponse(t, resB, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {MatchRoomRequiredStateStrict([]Event{{Type: "m.room.create", StateKey: ptr("")}})},
	}))

	// Conn C: create a list
	resC = alice.SlidingSync(t, sync3.Request{ConnID: "C", Lists: map[string]sync3.RequestList{
		"list": {
			Ranges: sync3.SliceRanges{{0, 10}},
		},
	}}, WithPos(resC.Pos))
	m.MatchResponse(t, resC, m.MatchList("list", m.MatchV3Count(1), m.MatchV3Ops(m.MatchV3SyncOp(0, 0, []string{roomID}))))
}
