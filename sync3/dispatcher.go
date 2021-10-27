package sync3

import (
	"encoding/json"
	"sync"

	"github.com/matrix-org/sync-v3/state"
	"github.com/tidwall/gjson"
)

const DispatcherAllUsers = "-"

type EventData struct {
	event     json.RawMessage
	roomID    string
	eventType string
	stateKey  *string
	content   gjson.Result
	timestamp uint64

	// TODO: remove or factor out
	userRoomData *UserRoomData
	// the absolute latest position for this event data. The NID for this event is guaranteed to
	// be <= this value.
	latestPos int64
}

type Receiver interface {
	OnNewEvent(event *EventData)
}

// Dispatches live events to user caches
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

// Load joined members into the dispatcher.
// MUST BE CALLED BEFORE V2 POLL LOOPS START.
func (d *Dispatcher) Load(store *state.Storage) error {
	// populate joined rooms tracker
	roomToJoinedUsers, err := store.AllJoinedMembers()
	if err != nil {
		return err
	}
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

	ed := &EventData{
		event:     event,
		roomID:    roomID,
		eventType: eventType,
		stateKey:  stateKey,
		content:   ev.Get("content"),
		latestPos: latestPos,
		timestamp: ev.Get("origin_server_ts").Uint(),
	}

	// update the tracker
	targetUser := ""
	if ed.eventType == "m.room.member" && ed.stateKey != nil {
		targetUser = *ed.stateKey
		// TODO: de-dupe joins in jrt else profile changes will results in 2x room IDs
		membership := ed.content.Get("membership").Str
		switch membership {
		case "join":
			d.jrt.UserJoinedRoom(targetUser, ed.roomID)
		case "ban":
			fallthrough
		case "leave":
			d.jrt.UserLeftRoom(targetUser, ed.roomID)
		}
	}

	// notify all people in this room
	userIDs := d.jrt.JoinedUsersForRoom(ed.roomID)

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
		l := d.userToReceiver[userID]
		if l != nil {
			l.OnNewEvent(ed)
		}
		if targetUser == userID {
			notifiedTarget = true
		}
	}
	if targetUser != "" && !notifiedTarget { // e.g invites where you aren't joined yet but need to know about it
		l := d.userToReceiver[targetUser]
		if l != nil {
			l.OnNewEvent(ed)
		}
	}
}
