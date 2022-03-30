package sync3

import (
	"encoding/json"
	"os"
	"sync"

	"github.com/matrix-org/sync-v3/sync3/caches"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

const DispatcherAllUsers = "-"

type Receiver interface {
	OnNewEvent(event *caches.EventData)
}

// Dispatches live events to caches
type Dispatcher struct {
	jrt              *JoinedRoomsTracker
	userToReceiver   map[string]Receiver
	userToReceiverMu *sync.RWMutex
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		jrt:              NewJoinedRoomsTracker(),
		userToReceiver:   make(map[string]Receiver),
		userToReceiverMu: &sync.RWMutex{},
	}
}

func (d *Dispatcher) IsUserJoined(userID, roomID string) bool {
	return d.jrt.IsUserJoined(userID, roomID)
}

// Load joined members into the dispatcher.
// MUST BE CALLED BEFORE V2 POLL LOOPS START.
func (d *Dispatcher) Startup(roomToJoinedUsers map[string][]string) error {
	// populate joined rooms tracker
	for roomID, userIDs := range roomToJoinedUsers {
		for _, userID := range userIDs {
			d.jrt.UserJoinedRoom(userID, roomID)
		}
	}
	return nil
}

func (d *Dispatcher) Unregister(userID string) {
	d.userToReceiverMu.Lock()
	defer d.userToReceiverMu.Unlock()
	delete(d.userToReceiver, userID)
}

func (d *Dispatcher) Register(userID string, r Receiver) {
	d.userToReceiverMu.Lock()
	defer d.userToReceiverMu.Unlock()
	if _, ok := d.userToReceiver[userID]; ok {
		logger.Warn().Str("user", userID).Msg("Dispatcher.Register: receiver already registered")
	}
	d.userToReceiver[userID] = r
}

// Called by v2 pollers when we receive new events
func (d *Dispatcher) OnNewEvents(
	roomID string, events []json.RawMessage, latestPos int64,
) {
	for _, event := range events {
		d.onNewEvent(roomID, event, latestPos)
	}
}

func (d *Dispatcher) onNewEvent(
	roomID string, event json.RawMessage, latestPos int64,
) {
	// parse the event to pull out fields we care about
	var stateKey *string
	ev := gjson.ParseBytes(event)
	if sk := ev.Get("state_key"); sk.Exists() {
		stateKey = &sk.Str
	}
	eventType := ev.Get("type").Str

	ed := &caches.EventData{
		Event:     event,
		RoomID:    roomID,
		EventType: eventType,
		StateKey:  stateKey,
		Content:   ev.Get("content"),
		LatestPos: latestPos,
		Timestamp: ev.Get("origin_server_ts").Uint(),
	}

	// update the tracker
	targetUser := ""
	membership := ""
	shouldForceInitial := false
	if ed.EventType == "m.room.member" && ed.StateKey != nil {
		targetUser = *ed.StateKey
		// TODO: de-dupe joins in jrt else profile changes will results in 2x room IDs
		membership = ed.Content.Get("membership").Str
		switch membership {
		case "join":
			if d.jrt.UserJoinedRoom(targetUser, ed.RoomID) {
				shouldForceInitial = true
			}
		case "ban":
			fallthrough
		case "leave":
			d.jrt.UserLeftRoom(targetUser, ed.RoomID)
		}
	}

	// notify all people in this room
	userIDs := d.jrt.JoinedUsersForRoom(ed.RoomID)

	// invoke listeners
	d.userToReceiverMu.RLock()
	defer d.userToReceiverMu.RUnlock()

	// global listeners (invoke before per-user listeners so caches can update)
	listener := d.userToReceiver[DispatcherAllUsers]
	if listener != nil {
		listener.OnNewEvent(ed)
	}

	// per-user listeners
	notifiedTarget := false
	for _, userID := range userIDs {
		edd := *ed
		if targetUser == userID {
			notifiedTarget = true
			if shouldForceInitial {
				edd.ForceInitial = true
			}
		}
		l := d.userToReceiver[userID]
		if l != nil {
			l.OnNewEvent(&edd)
		}
	}
	if targetUser != "" && !notifiedTarget { // e.g invites/leaves where you aren't joined yet but need to know about it
		// We expect invites to come down the invitee's poller, which triggers OnInvite code paths and
		// not normal event codepaths. We need the separate code path to ensure invite stripped state
		// is sent to the conn and not live data. Hence, if we get the invite event early from a different
		// connection, do not send it to the target, as they must wait for the invite on their poller.
		if membership != "invite" {
			if shouldForceInitial {
				ed.ForceInitial = true
			}
			l := d.userToReceiver[targetUser]
			if l != nil {
				l.OnNewEvent(ed)
			}
		}
	}
}
