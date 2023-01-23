package syncv3_test

import (
	"fmt"
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

func TestUnreadCountsUpdate(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	roomID := alice.CreateRoom(t, map[string]interface{}{
		"preset": "public_chat",
	})
	bob.JoinRoom(t, roomID, nil)
	eventID := bob.SendEventSynced(t, roomID, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"msgtype": "m.text",
			"body":    "Hello World",
		},
	})

	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		roomID: {
			m.MatchRoomNotificationCount(1),
		},
	}))
	alice.MustDoFunc(t, "POST", []string{"_matrix", "client", "v3", "rooms", roomID, "read_markers"}, WithJSONBody(t, map[string]interface{}{
		"m.fully_read": eventID,
		"m.read":       eventID,
	}))
	alice.SlidingSyncUntil(t, res.Pos, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, func(r *sync3.Response) error {
		room, ok := r.Rooms[roomID]
		if !ok {
			return fmt.Errorf("no room %s", roomID)
		}
		if room.NotificationCount != 0 {
			return fmt.Errorf("notif count = %d", room.NotificationCount)
		}
		return nil
	})
}
