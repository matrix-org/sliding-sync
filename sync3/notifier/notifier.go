package notifier

import (
	"context"
	"sync"
	"time"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/rs/zerolog"
)

type PeekingDevice struct {
	UserID   string
	DeviceID string
}

// Notifier will wake up sleeping requests when there is some new data.
// It does not tell requests what that data is, only the sync position which
// they can use to get at it. This is done to prevent races whereby we tell the caller
// the event, but the token has already advanced by the time they fetch it, resulting
// in missed events.
type Notifier struct {
	// A map of RoomID => Set<UserID> : Must only be accessed by the OnNewEvent goroutine
	roomIDToJoinedUsers map[string]userIDSet
	// A map of RoomID => Set<UserID> : Must only be accessed by the OnNewEvent goroutine
	roomIDToPeekingDevices map[string]peekingDeviceSet
	// Protects currPos and userStreams.
	streamLock *sync.Mutex
	// The latest sync position
	currPos sync3.Token
	// A map of user_id => device_id => UserStream which can be used to wake a given user's /sync request.
	userDeviceStreams map[string]map[string]*UserDeviceStream
	// The last time we cleaned out stale entries from the userStreams map
	lastCleanUpTime time.Time
	logger          zerolog.Logger
}

// NewNotifier creates a new notifier set to the given sync position.
// In order for this to be of any use, the Notifier needs to be told all rooms and
// the joined users within each of them by calling Notifier.Load()
func NewNotifier(currPos sync3.Token) *Notifier {
	return &Notifier{
		currPos:                currPos,
		roomIDToJoinedUsers:    make(map[string]userIDSet),
		roomIDToPeekingDevices: make(map[string]peekingDeviceSet),
		userDeviceStreams:      make(map[string]map[string]*UserDeviceStream),
		streamLock:             &sync.Mutex{},
		lastCleanUpTime:        time.Now(),
	}
}

// OnNewEvent is called when a new event is received from the room server. Must only be
// called from a single goroutine, to avoid races between updates which could set the
// current sync position incorrectly.
// Chooses which user sync streams to update by a provided *gomatrixserverlib.Event
// (based on the users in the event's room),
// a roomID directly, or a list of user IDs, prioritised by parameter ordering.
// posUpdate contains the latest position(s) for one or more types of events.
// If a position in posUpdate is 0, it means no updates are available of that type.
// Typically a consumer supplies a posUpdate with the latest sync position for the
// event type it handles, leaving other fields as 0.
func (n *Notifier) OnNewEvent(
	roomID, sender, eventType, stateKey, membership string, userIDs []string,
	posUpdate sync3.Token,
) {
	// update the current position then notify relevant /sync streams.
	// This needs to be done PRIOR to waking up users as they will read this value.
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.removeEmptyUserStreams()

	if eventType == "m.room.member" {
		// Map this event's room_id to a list of joined users, and wake them up.
		usersToNotify := n.joinedUsers(roomID)
		// Map this event's room_id to a list of peeking devices, and wake them up.
		peekingDevicesToNotify := n.PeekingDevices(roomID)
		// If this is an invite, also add in the invitee to this list.
		targetUserID := stateKey
		// Keep the joined user map up-to-date
		switch membership {
		case gomatrixserverlib.Invite:
			usersToNotify = append(usersToNotify, targetUserID)
		case gomatrixserverlib.Join:
			// Manually append the new user's ID so they get notified
			// along all members in the room
			usersToNotify = append(usersToNotify, targetUserID)
			n.AddJoinedUser(roomID, targetUserID)
		case gomatrixserverlib.Leave:
			fallthrough
		case gomatrixserverlib.Ban:
			n.RemoveJoinedUser(roomID, targetUserID)
		}

		n.wakeupUsers(usersToNotify, peekingDevicesToNotify, n.currPos)
	} else if roomID != "" {
		n.wakeupUsers(n.joinedUsers(roomID), n.PeekingDevices(roomID), n.currPos)
	} else if len(userIDs) > 0 {
		n.wakeupUsers(userIDs, nil, n.currPos)
	} else {
		n.logger.Warn().Str("pos_update", posUpdate.String()).Msg(
			"Notifier.OnNewEvent called but caller supplied no user to wake up",
		)
	}
}

func (n *Notifier) OnNewAccountData(
	userID string, posUpdate sync3.Token,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.wakeupUsers([]string{userID}, nil, posUpdate)
}

func (n *Notifier) OnNewPeek(
	roomID, userID, deviceID string,
	posUpdate sync3.Token,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.addPeekingDevice(roomID, userID, deviceID)

	// we don't wake up devices here given the roomserver consumer will do this shortly afterwards
	// by calling OnNewEvent.
}

func (n *Notifier) OnRetirePeek(
	roomID, userID, deviceID string,
	posUpdate sync3.Token,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.removePeekingDevice(roomID, userID, deviceID)

	// we don't wake up devices here given the roomserver consumer will do this shortly afterwards
	// by calling OnRetireEvent.
}

func (n *Notifier) OnNewSendToDevice(
	userID string, deviceIDs []string,
	posUpdate sync3.Token,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.wakeupUserDevice(userID, deviceIDs, n.currPos)
}

// OnNewReceipt updates the current position
func (n *Notifier) OnNewTyping(
	roomID string,
	posUpdate sync3.Token,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.wakeupUsers(n.joinedUsers(roomID), nil, n.currPos)
}

// OnNewReceipt updates the current position
func (n *Notifier) OnNewReceipt(
	roomID string,
	posUpdate sync3.Token,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.wakeupUsers(n.joinedUsers(roomID), nil, n.currPos)
}

func (n *Notifier) OnNewKeyChange(
	posUpdate sync3.Token, wakeUserID, keyChangeUserID string,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.wakeupUsers([]string{wakeUserID}, nil, n.currPos)
}

func (n *Notifier) OnNewInvite(
	posUpdate sync3.Token, wakeUserID string,
) {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.currPos.ApplyUpdates(posUpdate)
	n.wakeupUsers([]string{wakeUserID}, nil, n.currPos)
}

// GetListener returns a UserStreamListener that can be used to wait for
// updates for a user. Must be closed.
// notify for anything before sincePos
func (n *Notifier) GetListener(ctx context.Context, session sync3.Session) UserDeviceStreamListener {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	n.removeEmptyUserStreams()

	// TODO: session ID not device ID
	// TODO: Need to apply the filters so we don't wake up the device for data it won't see
	return n.fetchUserDeviceStream(session.UserID, session.DeviceID, true).GetListener(ctx)
}

// Load the membership states required to notify users correctly.
func (n *Notifier) Load(roomIDToUserIDs map[string][]string) {
	n.setUsersJoinedToRooms(roomIDToUserIDs)
}

// CurrentPosition returns the current sync position
func (n *Notifier) CurrentPosition() sync3.Token {
	n.streamLock.Lock()
	defer n.streamLock.Unlock()

	return n.currPos
}

// setUsersJoinedToRooms marks the given users as 'joined' to the given rooms, such that new events from
// these rooms will wake the given users /sync requests. This should be called prior to ANY calls to
// OnNewEvent (eg on startup) to prevent racing.
func (n *Notifier) setUsersJoinedToRooms(roomIDToUserIDs map[string][]string) {
	// This is just the bulk form of addJoinedUser
	for roomID, userIDs := range roomIDToUserIDs {
		if _, ok := n.roomIDToJoinedUsers[roomID]; !ok {
			n.roomIDToJoinedUsers[roomID] = make(userIDSet)
		}
		for _, userID := range userIDs {
			n.roomIDToJoinedUsers[roomID].add(userID)
		}
	}
}

// setPeekingDevices marks the given devices as peeking in the given rooms, such that new events from
// these rooms will wake the given devices' /sync requests. This should be called prior to ANY calls to
// OnNewEvent (eg on startup) to prevent racing.
func (n *Notifier) setPeekingDevices(roomIDToPeekingDevices map[string][]PeekingDevice) {
	// This is just the bulk form of addPeekingDevice
	for roomID, peekingDevices := range roomIDToPeekingDevices {
		if _, ok := n.roomIDToPeekingDevices[roomID]; !ok {
			n.roomIDToPeekingDevices[roomID] = make(peekingDeviceSet)
		}
		for _, peekingDevice := range peekingDevices {
			n.roomIDToPeekingDevices[roomID].add(peekingDevice)
		}
	}
}

// wakeupUsers will wake up the sync strems for all of the devices for all of the
// specified user IDs, and also the specified peekingDevices
func (n *Notifier) wakeupUsers(userIDs []string, peekingDevices []PeekingDevice, newPos sync3.Token) {
	for _, userID := range userIDs {
		for _, stream := range n.fetchUserStreams(userID) {
			if stream == nil {
				continue
			}
			stream.Broadcast(newPos) // wake up all goroutines Wait()ing on this stream
		}
	}

	for _, peekingDevice := range peekingDevices {
		if stream := n.fetchUserDeviceStream(peekingDevice.UserID, peekingDevice.DeviceID, false); stream != nil {
			stream.Broadcast(newPos) // wake up all goroutines Wait()ing on this stream
		}
	}
}

// wakeupUserDevice will wake up the sync stream for a specific user device. Other
// device streams will be left alone.
// nolint:unused
func (n *Notifier) wakeupUserDevice(userID string, deviceIDs []string, newPos sync3.Token) {
	for _, deviceID := range deviceIDs {
		if stream := n.fetchUserDeviceStream(userID, deviceID, false); stream != nil {
			stream.Broadcast(newPos) // wake up all goroutines Wait()ing on this stream
		}
	}
}

// fetchUserDeviceStream retrieves a stream unique to the given device. If makeIfNotExists is true,
// a stream will be made for this device if one doesn't exist and it will be returned. This
// function does not wait for data to be available on the stream.
// NB: Callers should have locked the mutex before calling this function.
func (n *Notifier) fetchUserDeviceStream(userID, deviceID string, makeIfNotExists bool) *UserDeviceStream {
	_, ok := n.userDeviceStreams[userID]
	if !ok {
		if !makeIfNotExists {
			return nil
		}
		n.userDeviceStreams[userID] = map[string]*UserDeviceStream{}
	}
	stream, ok := n.userDeviceStreams[userID][deviceID]
	if !ok {
		if !makeIfNotExists {
			return nil
		}
		if stream = NewUserDeviceStream(userID, deviceID, n.currPos); stream != nil {
			n.userDeviceStreams[userID][deviceID] = stream
		}
	}
	return stream
}

// fetchUserStreams retrieves all streams for the given user. If makeIfNotExists is true,
// a stream will be made for this user if one doesn't exist and it will be returned. This
// function does not wait for data to be available on the stream.
// NB: Callers should have locked the mutex before calling this function.
func (n *Notifier) fetchUserStreams(userID string) []*UserDeviceStream {
	user, ok := n.userDeviceStreams[userID]
	if !ok {
		return []*UserDeviceStream{}
	}
	streams := []*UserDeviceStream{}
	for _, stream := range user {
		streams = append(streams, stream)
	}
	return streams
}

// Not thread-safe: must be called on the OnNewEvent goroutine only
func (n *Notifier) AddJoinedUser(roomID, userID string) {
	if _, ok := n.roomIDToJoinedUsers[roomID]; !ok {
		n.roomIDToJoinedUsers[roomID] = make(userIDSet)
	}
	n.roomIDToJoinedUsers[roomID].add(userID)
}

// Not thread-safe: must be called on the OnNewEvent goroutine only
func (n *Notifier) RemoveJoinedUser(roomID, userID string) {
	if _, ok := n.roomIDToJoinedUsers[roomID]; !ok {
		n.roomIDToJoinedUsers[roomID] = make(userIDSet)
	}
	n.roomIDToJoinedUsers[roomID].remove(userID)
}

// Not thread-safe: must be called on the OnNewEvent goroutine only
func (n *Notifier) joinedUsers(roomID string) (userIDs []string) {
	if _, ok := n.roomIDToJoinedUsers[roomID]; !ok {
		return
	}
	return n.roomIDToJoinedUsers[roomID].values()
}

// Not thread-safe: must be called on the OnNewEvent goroutine only
func (n *Notifier) addPeekingDevice(roomID, userID, deviceID string) {
	if _, ok := n.roomIDToPeekingDevices[roomID]; !ok {
		n.roomIDToPeekingDevices[roomID] = make(peekingDeviceSet)
	}
	n.roomIDToPeekingDevices[roomID].add(PeekingDevice{UserID: userID, DeviceID: deviceID})
}

// Not thread-safe: must be called on the OnNewEvent goroutine only
// nolint:unused
func (n *Notifier) removePeekingDevice(roomID, userID, deviceID string) {
	if _, ok := n.roomIDToPeekingDevices[roomID]; !ok {
		n.roomIDToPeekingDevices[roomID] = make(peekingDeviceSet)
	}
	n.roomIDToPeekingDevices[roomID].remove(PeekingDevice{UserID: userID, DeviceID: deviceID})
}

// Not thread-safe: must be called on the OnNewEvent goroutine only
func (n *Notifier) PeekingDevices(roomID string) (peekingDevices []PeekingDevice) {
	if _, ok := n.roomIDToPeekingDevices[roomID]; !ok {
		return
	}
	return n.roomIDToPeekingDevices[roomID].values()
}

// removeEmptyUserStreams iterates through the user stream map and removes any
// that have been empty for a certain amount of time. This is a crude way of
// ensuring that the userStreams map doesn't grow forver.
// This should be called when the notifier gets called for whatever reason,
// the function itself is responsible for ensuring it doesn't iterate too
// often.
// NB: Callers should have locked the mutex before calling this function.
func (n *Notifier) removeEmptyUserStreams() {
	// Only clean up  now and again
	now := time.Now()
	if n.lastCleanUpTime.Add(time.Minute).After(now) {
		return
	}
	n.lastCleanUpTime = now

	deleteBefore := now.Add(-5 * time.Minute)
	for user, byUser := range n.userDeviceStreams {
		for device, stream := range byUser {
			if stream.TimeOfLastNonEmpty().Before(deleteBefore) {
				delete(n.userDeviceStreams[user], device)
			}
			if len(n.userDeviceStreams[user]) == 0 {
				delete(n.userDeviceStreams, user)
			}
		}
	}
}

// A string set, mainly existing for improving clarity of structs in this file.
type userIDSet map[string]bool

func (s userIDSet) add(str string) {
	s[str] = true
}

func (s userIDSet) remove(str string) {
	delete(s, str)
}

func (s userIDSet) values() (vals []string) {
	for str := range s {
		vals = append(vals, str)
	}
	return
}

// A set of PeekingDevices, similar to userIDSet

type peekingDeviceSet map[PeekingDevice]bool

func (s peekingDeviceSet) add(d PeekingDevice) {
	s[d] = true
}

// nolint:unused
func (s peekingDeviceSet) remove(d PeekingDevice) {
	delete(s, d)
}

func (s peekingDeviceSet) values() (vals []PeekingDevice) {
	for d := range s {
		vals = append(vals, d)
	}
	return
}
