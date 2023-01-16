package handler

import (
	"context"
	"encoding/json"
	"runtime/trace"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/tidwall/gjson"
)

var (
	// The max number of events the client is eligible to read (unfiltered) which we are willing to
	// buffer on this connection. Too large and we consume lots of memory. Too small and busy accounts
	// will trip the connection knifing.
	MaxPendingEventUpdates = 2000
)

// Contains code for processing live updates. Split out from connstate because they concern different
// code paths. Relies on ConnState for various list/sort/subscription operations.
type connStateLive struct {
	*ConnState

	// roomID -> latest load pos
	loadPositions map[string]int64

	// A channel which the dispatcher uses to send updates to the conn goroutine
	// Consumed when the conn is read. There is a limit to how many updates we will store before
	// saying the client is dead and clean up the conn.
	updates    chan caches.Update
	bufferFull bool
}

// Called when there is an update from the user cache. This callback fires when the server gets a new event and determines this connection MAY be
// interested in it (e.g the client is joined to the room or it's an invite, etc).
// We need to move this data onto a channel for onIncomingRequest to consume later.
func (s *connStateLive) onUpdate(up caches.Update) {
	if s.bufferFull {
		return
	}
	select {
	case s.updates <- up:
	case <-time.After(5 * time.Second):
		logger.Warn().Interface("update", up).Str("user", s.userID).Msg(
			"cannot send update to connection, buffer exceeded. Destroying connection.",
		)
		s.bufferFull = true
		s.Destroy()
	}
}

// live update waits for new data and populates the response given when new data arrives.
func (s *connStateLive) liveUpdate(
	ctx context.Context, req *sync3.Request, ex extensions.Request, isInitial bool,
	response *sync3.Response,
) {
	// we need to ensure that we keep consuming from the updates channel, even if they want a response
	// immediately. If we have new list data we won't wait, but if we don't then we need to be able to
	// catch-up to the current head position, hence giving 100ms grace period for processing.
	if req.TimeoutMSecs() < 100 {
		req.SetTimeoutMSecs(100)
	}
	// block until we get a new event, with appropriate timeout
	startTime := time.Now()
	for response.ListOps() == 0 && len(response.Rooms) == 0 && !response.Extensions.HasData(isInitial) {
		timeToWait := time.Duration(req.TimeoutMSecs()) * time.Millisecond
		timeWaited := time.Since(startTime)
		timeLeftToWait := timeToWait - timeWaited
		if timeLeftToWait < 0 {
			logger.Trace().Str("user", s.userID).Str("time_waited", timeWaited.String()).Msg("liveUpdate: timed out")
			return
		}
		logger.Trace().Str("user", s.userID).Str("dur", timeLeftToWait.String()).Msg("liveUpdate: no response data yet; blocking")
		select {
		case <-ctx.Done(): // client has given up
			logger.Trace().Str("user", s.userID).Msg("liveUpdate: client gave up")
			trace.Logf(ctx, "liveUpdate", "context cancelled")
			return
		case <-time.After(timeLeftToWait): // we've timed out
			logger.Trace().Str("user", s.userID).Msg("liveUpdate: timed out")
			trace.Logf(ctx, "liveUpdate", "timed out after %v", timeLeftToWait)
			return
		case update := <-s.updates:
			trace.Logf(ctx, "liveUpdate", "process live update")
			updateWillReturnResponse := s.processLiveUpdate(ctx, update, response)
			// pass event to extensions AFTER processing
			s.extensionsHandler.HandleLiveUpdate(update, ex, &response.Extensions, updateWillReturnResponse, isInitial)
			// if there's more updates and we don't have lots stacked up already, go ahead and process another
			for len(s.updates) > 0 && response.ListOps() < 50 {
				update = <-s.updates
				willReturn := s.processLiveUpdate(ctx, update, response)
				if willReturn {
					updateWillReturnResponse = true
				}
				s.extensionsHandler.HandleLiveUpdate(update, ex, &response.Extensions, updateWillReturnResponse, isInitial)
			}
			// Add membership events for users sending typing notifications
			if response.Extensions.Typing != nil && response.Extensions.Typing.HasData(isInitial) {
				s.lazyLoadTypingMembers(ctx, response)
			}
		}
	}
	logger.Trace().Str("user", s.userID).Int("subs", len(response.Rooms)).Msg("liveUpdate: returning")
	// TODO: op consolidation
}

func (s *connStateLive) lazyLoadTypingMembers(ctx context.Context, response *sync3.Response) {
	for roomID, typingEvent := range response.Extensions.Typing.Rooms {
		if !s.lazyCache.IsLazyLoading(roomID) {
			continue
		}
		room, ok := response.Rooms[roomID]
		if !ok {
			room = sync3.Room{}
		}
		typingUsers := gjson.GetBytes(typingEvent, "content.user_ids")
		for _, typingUserID := range typingUsers.Array() {
			if s.lazyCache.IsSet(roomID, typingUserID.Str) {
				// client should already know about this member
				continue
			}
			// load the state event
			memberEvent := s.globalCache.LoadStateEvent(ctx, roomID, s.loadPosition, "m.room.member", typingUserID.Str)
			if memberEvent != nil {
				room.RequiredState = append(room.RequiredState, memberEvent)
				s.lazyCache.AddUser(roomID, typingUserID.Str)
			}
		}
		// only add the room if we have membership events
		if len(room.RequiredState) > 0 {
			response.Rooms[roomID] = room
		}
	}
}

func (s *connStateLive) processLiveUpdate(ctx context.Context, up caches.Update, response *sync3.Response) bool {
	internal.Assert("processLiveUpdate: response list length != internal list length", s.lists.Len() == len(response.Lists))
	internal.Assert("processLiveUpdate: request list length != internal list length", s.lists.Len() == len(s.muxedReq.Lists))

	// for initial rooms e.g a room comes into the window or a subscription now exists
	builder := NewRoomsBuilder()

	// do global connection updates (e.g adding/removing rooms from allRooms)
	delta := s.processGlobalUpdates(ctx, builder, up)

	// process room subscriptions
	hasUpdates := s.processUpdatesForSubscriptions(ctx, builder, up)

	// do per-list updates (e.g resorting, adding/removing rooms which no longer match filter)
	for _, listDelta := range delta.Lists {
		index := listDelta.ListIndex
		list := s.lists.Get(index)
		reqList := s.muxedReq.Lists[index]
		updates := s.processLiveUpdateForList(ctx, builder, up, listDelta.Op, &reqList, list, &response.Lists[index])
		if updates {
			hasUpdates = true
		}
	}

	// add in initial rooms FIRST as we replace whatever is in the rooms key for these rooms.
	// If we do it after appending live updates then we can lose updates because we replace what
	// we accumulated.
	rooms := s.buildRooms(ctx, builder.BuildSubscriptions())
	for roomID, room := range rooms {
		response.Rooms[roomID] = room
		// remember what point we snapshotted this room, incase we see live events which we have
		// already snapshotted here.
		s.loadPositions[roomID] = s.loadPosition
	}

	roomUpdate, _ := up.(caches.RoomUpdate)
	roomEventUpdate, _ := up.(*caches.RoomEventUpdate)

	// TODO: find a better way to determine if the triggering event should be included e.g ask the lists?
	if hasUpdates && roomEventUpdate != nil {
		// include this update in the rooms response TODO: filters on event type?
		userRoomData := roomUpdate.UserRoomMetadata()
		r := response.Rooms[roomUpdate.RoomID()]
		r.HighlightCount = int64(userRoomData.HighlightCount)
		r.NotificationCount = int64(userRoomData.NotificationCount)
		if roomEventUpdate != nil && roomEventUpdate.EventData.Event != nil {
			r.NumLive++
			advancedPastEvent := false
			if roomEventUpdate.EventData.LatestPos <= s.loadPositions[roomEventUpdate.RoomID()] {
				// this update has been accounted for by the initial:true room snapshot
				advancedPastEvent = true
			}
			s.loadPositions[roomEventUpdate.RoomID()] = roomEventUpdate.EventData.LatestPos
			// we only append to the timeline if we haven't already got this event. This can happen when:
			// - 2 live events for a room mid-connection
			// - next request bumps a room from outside to inside the window
			// - the initial:true room from BuildSubscriptions contains the latest live events in the timeline as it's pulled from the DB
			// - we then process the live events in turn which adds them again.
			if !advancedPastEvent {
				roomIDtoTimeline := s.userCache.AnnotateWithTransactionIDs(s.deviceID, map[string][]json.RawMessage{
					roomEventUpdate.RoomID(): []json.RawMessage{roomEventUpdate.EventData.Event},
				})
				r.Timeline = append(r.Timeline, roomIDtoTimeline[roomEventUpdate.RoomID()]...)
				roomID := roomEventUpdate.RoomID()
				sender := roomEventUpdate.EventData.Sender
				if s.lazyCache.IsLazyLoading(roomID) && !s.lazyCache.IsSet(roomID, sender) {
					// load the state event
					memberEvent := s.globalCache.LoadStateEvent(context.Background(), roomID, s.loadPosition, "m.room.member", sender)
					if memberEvent != nil {
						r.RequiredState = append(r.RequiredState, memberEvent)
						s.lazyCache.AddUser(roomID, sender)
					}
				}
			}
		}
		response.Rooms[roomUpdate.RoomID()] = r
	}

	if roomUpdate != nil {
		// try to find this room in the response. If it's there, then we may need to update some fields.
		// there's no guarantees that the room will be in the response if say the event caused it to move
		// off a list.
		thisRoom, exists := response.Rooms[roomUpdate.RoomID()]
		if exists {
			if delta.RoomNameChanged {
				metadata := roomUpdate.GlobalRoomMetadata()
				metadata.RemoveHero(s.userID)
				thisRoom.Name = internal.CalculateRoomName(metadata, 5) // TODO: customisable?
			}
			if delta.InviteCountChanged {
				thisRoom.InvitedCount = roomUpdate.GlobalRoomMetadata().InviteCount
			}
			if delta.JoinCountChanged {
				thisRoom.JoinedCount = roomUpdate.GlobalRoomMetadata().JoinCount
			}

			response.Rooms[roomUpdate.RoomID()] = thisRoom
		}
		if delta.HighlightCountChanged || delta.NotificationCountChanged {
			if !exists {
				// we need to make this room exist. Other deltas are caused by events so the room exists,
				// but highlight/notif counts are silent
				thisRoom = sync3.Room{}
			}
			thisRoom.NotificationCount = int64(roomUpdate.UserRoomMetadata().NotificationCount)
			thisRoom.HighlightCount = int64(roomUpdate.UserRoomMetadata().HighlightCount)
			response.Rooms[roomUpdate.RoomID()] = thisRoom
		}
	}
	return hasUpdates
}

func (s *connStateLive) processUpdatesForSubscriptions(ctx context.Context, builder *RoomsBuilder, up caches.Update) (hasUpdates bool) {
	rup, ok := up.(caches.RoomUpdate)
	if !ok {
		return false
	}
	// if we have an existing confirmed subscription for this room, then there's nothing to do.
	if _, exists := s.roomSubscriptions[rup.RoomID()]; exists {
		return true // this room exists as a subscription so we'll handle it correctly
	}
	// did the client ask to subscribe to this room?
	_, ok = s.muxedReq.RoomSubscriptions[rup.RoomID()]
	if !ok {
		return false // the client does not want a subscription to this room, so do nothing.
	}
	// the user does not have a subscription to this room yet but wants one, try to add it.
	// this will do join checks for us.
	s.buildRoomSubscriptions(ctx, builder, []string{rup.RoomID()}, nil)

	// if we successfully made the subscription, it will now exist in the confirmed subscriptions map
	_, exists := s.roomSubscriptions[rup.RoomID()]
	return exists
}

// this function does any updates which apply to the connection, regardless of which lists/subs exist.
func (s *connStateLive) processGlobalUpdates(ctx context.Context, builder *RoomsBuilder, up caches.Update) (delta sync3.RoomDelta) {
	rup, ok := up.(caches.RoomUpdate)
	if ok {
		delta = s.lists.SetRoom(sync3.RoomConnMetadata{
			RoomMetadata: *rup.GlobalRoomMetadata(),
			UserRoomData: *rup.UserRoomMetadata(),
		})
	}

	roomEventUpdate, ok := up.(*caches.RoomEventUpdate)
	if ok {
		// TODO: we should do this check before lists.SetRoom
		if roomEventUpdate.EventData.LatestPos <= s.loadPosition {
			return // if this update is in the past then ignore it
		}
		s.loadPosition = roomEventUpdate.EventData.LatestPos
	}
	return
}

func (s *connStateLive) processLiveUpdateForList(
	ctx context.Context, builder *RoomsBuilder, up caches.Update, listOp sync3.ListOp,
	reqList *sync3.RequestList, intList *sync3.FilteredSortableRooms, resList *sync3.ResponseList,
) (hasUpdates bool) {
	switch update := up.(type) {
	case *caches.RoomEventUpdate:
		logger.Trace().Str("user", s.userID).Str("type", update.EventData.EventType).Msg("received event update")
		if update.EventData.ForceInitial {
			// add room to sub: this applies for when we track all rooms too as we want joins/etc to come through with initial data
			subID := builder.AddSubscription(reqList.RoomSubscription)
			builder.AddRoomsToSubscription(subID, []string{update.RoomID()})
		}
	case *caches.UnreadCountUpdate:
		logger.Trace().Str("user", s.userID).Str("room", update.RoomID()).Msg("received unread count update")
		if !update.HasCountDecreased {
			// if the count increases then we'll notify the user for the event which increases the count, hence
			// do nothing. We only care to notify the user when the counts decrease.
			return false
		}
	}

	// only room updates can cause the rooms to reshuffle e.g events, room account data, tags
	// because we need a room ID to move.
	rup, ok := up.(caches.RoomUpdate)
	if !ok {
		return false
	}
	ops, hasUpdates := s.resort(
		ctx, builder, reqList, intList, rup.RoomID(), listOp,
	)
	resList.Ops = append(resList.Ops, ops...)

	if !hasUpdates {
		hasUpdates = len(resList.Ops) > 0
	}

	return hasUpdates
}

// Resort should be called after a specific room has been modified in `intList`.
func (s *connStateLive) resort(
	ctx context.Context, builder *RoomsBuilder,
	reqList *sync3.RequestList, intList *sync3.FilteredSortableRooms, roomID string,
	listOp sync3.ListOp,
) (ops []sync3.ResponseOp, didUpdate bool) {
	if reqList.ShouldGetAllRooms() {
		// no need to sort this list as we get all rooms
		// no need to calculate ops as we get all rooms
		// no need to send initial state for some rooms as we already sent initial state for all rooms
		if listOp == sync3.ListOpAdd {
			intList.Add(roomID)
			// ensure we send data when the user joins a new room
			subID := builder.AddSubscription(reqList.RoomSubscription)
			builder.AddRoomsToSubscription(subID, []string{roomID})
		} else if listOp == sync3.ListOpDel {
			intList.Remove(roomID)
		}
		return nil, true
	}

	ops, subs := sync3.CalculateListOps(reqList, intList, roomID, listOp)
	if len(subs) > 0 { // handle rooms which have just come into the window
		subID := builder.AddSubscription(reqList.RoomSubscription)
		builder.AddRoomsToSubscription(subID, subs)
	}

	// there are updates if we have ops, new subs or if the triggering room is inside the range still
	hasUpdates := len(ops) > 0 || len(subs) > 0
	if !hasUpdates {
		roomIndex, ok := intList.IndexOf(roomID)
		if ok {
			_, isInside := reqList.Ranges.Inside(int64(roomIndex))
			hasUpdates = isInside
		}
	}
	return ops, hasUpdates
}
