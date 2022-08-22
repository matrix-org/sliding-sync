package syncv3

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

type FlushEnum int

var (
	NoFlush FlushEnum = 1
	Flush   FlushEnum = 2
)

type testRig struct {
	V2     *testV2Server
	V3     *testV3Server
	tokens map[string]string
}

func (r *testRig) Finish() {
	r.V2.close()
	r.V3.close()
}

func (r *testRig) Token(userID string) string {
	return r.tokens[userID]
}

func (r *testRig) SetupV2RoomsForUser(t *testing.T, v2UserID string, f FlushEnum, data map[string]RoomDescriptor) {
	// allocate a user
	_, userExists := r.tokens[v2UserID]
	if !userExists {
		r.tokens[v2UserID] = "access_token_for_" + v2UserID
		r.V2.addAccount(v2UserID, r.tokens[v2UserID])
	}
	inviteRooms := make(map[string]sync2.SyncV2InviteResponse)
	joinRooms := make(map[string]sync2.SyncV2JoinResponse)
	for roomID, descriptor := range data {
		if descriptor.MembershipOfSyncer == "" {
			descriptor.MembershipOfSyncer = "join"
		}
		switch descriptor.MembershipOfSyncer {
		case "invite":
			inviteRooms[roomID] = sync2.SyncV2InviteResponse{
				InviteState: sync2.EventsResponse{
					Events: []json.RawMessage{testutils.NewStateEvent(t, "m.room.member", v2UserID, "@inviter:localhost", map[string]interface{}{
						"membership": "invite",
					})},
				},
			}
		case "join":
			creator := descriptor.Creator
			if creator == "" {
				creator = v2UserID
			}
			timestamp := time.Now()
			// configure the room
			var timeline []json.RawMessage
			if descriptor.IsEncrypted {
				timeline = append(timeline, testutils.NewStateEvent(
					t, "m.room.encryption", "", creator, map[string]interface{}{
						"algorithm":            "m.megolm.v1.aes-sha2",
						"rotation_period_ms":   604800000,
						"rotation_period_msgs": 100,
					},
				))
			}
			if descriptor.Name != "" {
				timeline = append(timeline, testutils.NewStateEvent(
					t, "m.room.name", "", creator, map[string]interface{}{
						"name": descriptor.Name,
					},
				))
			}
			for _, userID := range descriptor.InvitedUsers {
				timeline = append(timeline, testutils.NewStateEvent(
					t, "m.room.member", userID, creator, map[string]interface{}{
						"membership": "invite",
					},
				))
			}
			for _, userID := range descriptor.JoinedUsers {
				timeline = append(timeline, testutils.NewJoinEvent(
					t, userID,
				))
			}
			var stateBlock []json.RawMessage
			if descriptor.RoomType != "" {
				stateBlock = createRoomStateWithCreateEvent(t, creator,
					testutils.NewStateEvent(t, "m.room.create", "", creator, map[string]interface{}{"creator": creator, "type": descriptor.RoomType}, testutils.WithTimestamp(timestamp)),
					timestamp)
			} else {
				stateBlock = createRoomState(t, creator, timestamp)
			}
			joinRooms[roomID] = sync2.SyncV2JoinResponse{
				State: sync2.EventsResponse{
					Events: stateBlock,
				},
				Timeline: sync2.TimelineResponse{
					Events: timeline,
				},
			}
			if len(descriptor.Tags) > 0 {
				tagContent := make(map[string]interface{})
				for tagName, order := range descriptor.Tags {
					tagContent[tagName] = map[string]interface{}{
						"order": order,
					}
				}
				jr := joinRooms[roomID]
				jr.AccountData = sync2.EventsResponse{
					Events: []json.RawMessage{testutils.NewAccountData(
						t, "m.tag", tagContent,
					)},
				}
				joinRooms[roomID] = jr
			}
		default:
			t.Fatalf("unknown value for descriptor.MembershipOfSyncer")
		}
	}
	r.V2.queueResponse(v2UserID, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Invite: inviteRooms,
			Join:   joinRooms,
		},
	})
	if f == Flush {
		if !userExists {
			// do a single request to flush it
			_ = r.V3.mustDoV3Request(t, r.tokens[v2UserID], sync3.Request{})
		} else {
			// there is already a poller running for this user, wait for it to get the data.
			r.V2.waitUntilEmpty(t, v2UserID)
		}
	}
}

func (r *testRig) JoinRoom(t *testing.T, userID, roomID string) {
	r.FlushEvent(t, userID, roomID, testutils.NewJoinEvent(t, userID))
}

func (r *testRig) EncryptRoom(t *testing.T, userID, roomID string) {
	r.FlushEvent(t, userID, roomID, testutils.NewStateEvent(
		t, "m.room.encryption", "", userID, map[string]interface{}{
			"algorithm":            "m.megolm.v1.aes-sha2",
			"rotation_period_ms":   604800000,
			"rotation_period_msgs": 100,
		},
	))
}

func (r *testRig) FlushEvent(t *testing.T, userID, roomID string, event json.RawMessage) {
	r.V2.queueResponse(userID, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: roomID,
				events: []json.RawMessage{event},
			}),
		},
	})
	r.V2.waitUntilEmpty(t, userID)
}

func (r *testRig) Room(roomID string) *TestRoom {
	return nil
}

func NewTestRig(t testutils.TestBenchInterface) *testRig {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	rig := &testRig{
		V2:     v2,
		V3:     runTestServer(t, v2, pqString),
		tokens: make(map[string]string),
	}
	return rig
}

type RoomDescriptor struct {
	Creator            string
	JoinedUsers        []string
	InvitedUsers       []string
	MembershipOfSyncer string
	IsEncrypted        bool
	RoomType           string
	Name               string
	Tags               map[string]float64
}

type TestRoom struct {
}
