package handler

import (
	"context"
	"encoding/json"
	"runtime/trace"
	"strings"
	"time"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/caches"
	"github.com/matrix-org/sync-v3/sync3/extensions"
)

var (
	// The max number of events the client is eligible to read (unfiltered) which we are willing to
	// buffer on this connection. Too large and we consume lots of memory. Too small and busy accounts
	// will trip the connection knifing.
	MaxPendingEventUpdates = 200
)

// Contains code for processing live updates. Split out from connstate because they concern different
// code paths. Relies on ConnState for various list/sort/subscription operations.
type connStateLive struct {
	*ConnState

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
		}
	}
	logger.Trace().Str("user", s.userID).Int("subs", len(response.Rooms)).Msg("liveUpdate: returning")
	// TODO: op consolidation
}

func (s *connStateLive) processLiveUpdate(ctx context.Context, up caches.Update, response *sync3.Response) bool {
	hasUpdates := false // true if this update results in a response
	internal.Assert("processLiveUpdate: response list length != internal list length", s.lists.Len() == len(response.Lists))
	internal.Assert("processLiveUpdate: request list length != internal list length", s.lists.Len() == len(s.muxedReq.Lists))

	// for initial rooms e.g a room comes into the window or a subscription now exists
	builder := NewRoomsBuilder()

	// do global connection updates (e.g adding/removing rooms from allRooms)
	s.processGlobalUpdates(ctx, builder, up)

	var roomNameUpdated bool
	// process room subscriptions
	roomUpdate, ok := up.(*caches.RoomEventUpdate)
	if ok {
		if _, ok = s.roomSubscriptions[roomUpdate.RoomID()]; ok {
			// there is a subscription for this room
			hasUpdates = true
		}
		sameRoomName := s.lists.UpdateRoom(roomUpdate.GlobalRoomMetadata())
		roomNameUpdated = !sameRoomName
		if roomNameUpdated {
			// update the canonicalised name for this room so we can sort it correctly
			// TODO: this should probably be done in UserCache tbh...
			urd := roomUpdate.UserRoomMetadata()
			urd.CanonicalisedName = strings.ToLower(
				strings.Trim(internal.CalculateRoomName(roomUpdate.GlobalRoomMetadata(), 5), "#!():_@"),
			)
		}
	}

	// do per-list updates (e.g resorting, adding/removing rooms which no longer match filter)
	s.lists.ForEach(func(index int, list *sync3.FilteredSortableRooms) {
		reqList := s.muxedReq.Lists[index]
		updates := s.processLiveUpdateForList(ctx, builder, up, &reqList, list, &response.Lists[index])
		if updates {
			hasUpdates = true
		}
	})

	if hasUpdates && roomUpdate != nil {
		// include this update in the rooms response TODO: filters on event type?
		userRoomData := s.userCache.LoadRoomData(roomUpdate.RoomID()) // TODO: don't do this as we have a ref in live code
		r := response.Rooms[roomUpdate.RoomID()]
		r.HighlightCount = int64(userRoomData.HighlightCount)
		r.NotificationCount = int64(userRoomData.NotificationCount)
		if roomUpdate.EventData.Event != nil {
			r.Timeline = append(r.Timeline, s.userCache.AnnotateWithTransactionIDs([]json.RawMessage{
				roomUpdate.EventData.Event,
			})...)
		}
		response.Rooms[roomUpdate.RoomID()] = r
	}

	// add in initial rooms
	rooms := s.buildRooms(ctx, builder.BuildSubscriptions())
	for roomID, room := range rooms {
		response.Rooms[roomID] = room
	}
	if roomNameUpdated && roomUpdate != nil {
		// try to find this room in the response. If it's there, then we need to inject a new room name.
		// there's no guarantees that the room will be in the response if say the event caused it to move
		// off a list.
		room, exists := response.Rooms[roomUpdate.RoomID()]
		if exists {
			room.Name = internal.CalculateRoomName(roomUpdate.GlobalRoomMetadata(), 5) // TODO: customisable?
		}
		response.Rooms[roomUpdate.RoomID()] = room
	}
	return hasUpdates
}

// this function does any updates which apply to the connection, regardless of which lists/subs exist.
func (s *connStateLive) processGlobalUpdates(ctx context.Context, builder *RoomsBuilder, up caches.Update) {
	switch update := up.(type) {
	case *caches.RoomEventUpdate:
		// keep track of the latest stream position
		if update.EventData.LatestPos > s.loadPosition {
			s.loadPosition = update.EventData.LatestPos
			// newly joined rooms will be flagged correctly in lists, but not subscriptions, so check them now.
			if _, exists := s.roomSubscriptions[update.EventData.RoomID]; exists {
				break // this room exists as a subscription so we'll handle it correctly
			}
			_, ok := s.muxedReq.RoomSubscriptions[update.EventData.RoomID]
			if !ok {
				break // the client does not want a subscription to this room, so do nothing.
			}
			// the user does not have a subscription to this room yet but wants one, try to add it.
			// this will do join checks for us.
			s.buildRoomSubscriptions(builder, []string{update.EventData.RoomID}, nil)
		}
	case *caches.InviteUpdate:
		if update.Retired {
			// remove the room from all rooms
			logger.Trace().Str("user", s.userID).Str("room", update.RoomID()).Msg("processGlobalUpdates: room was retired")
			s.lists.RemoveRoom(update.RoomID())
		} else {
			metadata := update.InviteData.RoomMetadata()
			urd := *update.UserRoomMetadata()
			urd.CanonicalisedName = strings.ToLower(
				strings.Trim(internal.CalculateRoomName(metadata, 5), "#!():_@"),
			)
			s.lists.AddRoomIfNotExists(sync3.RoomConnMetadata{
				RoomMetadata: *metadata,
				UserRoomData: urd,
			})
		}
	}
}

func (s *connStateLive) processLiveUpdateForList(
	ctx context.Context, builder *RoomsBuilder, up caches.Update, reqList *sync3.RequestList, intList *sync3.FilteredSortableRooms, resList *sync3.ResponseList,
) (hasUpdates bool) {
	roomUpdate, ok := up.(caches.RoomUpdate)
	if ok { // update the internal lists - this may remove rooms if the room no longer matches a filter
		// see if the latest room metadata means we delete a room, else update our state
		deletedIndex := intList.UpdateGlobalRoomMetadata(roomUpdate.GlobalRoomMetadata())
		if op := reqList.WriteDeleteOp(deletedIndex); op != nil {
			resList.Ops = append(resList.Ops, op)
			hasUpdates = true
		}
		// see if the latest user room metadata means we delete a room (e.g it transition from dm to non-dm)
		// modify notification counts, DM-ness, etc
		deletedIndex = intList.UpdateUserRoomMetadata(roomUpdate.RoomID(), roomUpdate.UserRoomMetadata())
		if op := reqList.WriteDeleteOp(deletedIndex); op != nil {
			resList.Ops = append(resList.Ops, op)
			hasUpdates = true
		}
	}

	switch update := up.(type) {
	case *caches.RoomEventUpdate:
		logger.Trace().Str("user", s.userID).Str("type", update.EventData.EventType).Msg("received event update")
		ops, didUpdate := s.processIncomingEventForList(ctx, builder, update, reqList, intList)
		if didUpdate {
			hasUpdates = true
		}
		resList.Ops = append(resList.Ops, ops...)
	case *caches.UnreadCountUpdate:
		logger.Trace().Str("user", s.userID).Str("room", update.RoomID()).Msg("received unread count update")
		ops, didUpdate := s.processUnreadCountUpdateForList(ctx, builder, update, reqList, intList)
		if didUpdate {
			hasUpdates = true
		}
		resList.Ops = append(resList.Ops, ops...)
	case *caches.InviteUpdate:
		logger.Trace().Str("user", s.userID).Str("room", update.RoomID()).Msg("received invite update")
		if update.Retired {
			// remove the room from this list if need be
			deletedIndex := intList.Remove(update.RoomID())
			if op := reqList.WriteDeleteOp(deletedIndex); op != nil {
				resList.Ops = append(resList.Ops, op)
				hasUpdates = true
			}
		} else {
			roomUpdate := &caches.RoomEventUpdate{
				RoomUpdate: update.RoomUpdate,
				EventData:  update.InviteData.InviteEvent,
			}
			ops, didUpdate := s.processIncomingEventForList(ctx, builder, roomUpdate, reqList, intList)
			resList.Ops = append(resList.Ops, ops...)
			if didUpdate {
				hasUpdates = true
			}
		}
	}

	if !hasUpdates {
		hasUpdates = len(resList.Ops) > 0
	}

	return hasUpdates
}

func (s *connStateLive) processUnreadCountUpdateForList(
	ctx context.Context, builder *RoomsBuilder, up *caches.UnreadCountUpdate, reqList *sync3.RequestList, intList *sync3.FilteredSortableRooms,
) (ops []sync3.ResponseOp, didUpdate bool) {
	if !up.HasCountDecreased {
		// if the count increases then we'll notify the user for the event which increases the count, hence
		// do nothing. We only care to notify the user when the counts decrease.
		return nil, false
	}

	fromIndex, ok := intList.IndexOf(up.RoomID())
	if !ok {
		return nil, false
	}
	return s.resort(ctx, builder, reqList, intList, up.RoomID(), fromIndex, false)
}

func (s *connStateLive) processIncomingEventForList(
	ctx context.Context, builder *RoomsBuilder, update *caches.RoomEventUpdate, reqList *sync3.RequestList, intList *sync3.FilteredSortableRooms,
) (ops []sync3.ResponseOp, didUpdate bool) {
	fromIndex, ok := intList.IndexOf(update.RoomID())
	newlyAdded := false
	if !ok {
		// the user may have just joined the room hence not have an entry in this list yet.
		fromIndex = int(intList.Len())
		roomMetadata := update.GlobalRoomMetadata()
		roomMetadata.RemoveHero(s.userID)
		urd := *update.UserRoomMetadata()
		urd.CanonicalisedName = strings.ToLower(
			strings.Trim(internal.CalculateRoomName(roomMetadata, 5), "#!():_@"),
		)
		newRoomConn := sync3.RoomConnMetadata{
			RoomMetadata: *roomMetadata,
			UserRoomData: urd,
		}
		if !intList.Add(newRoomConn) {
			// we didn't add this room to the list so we don't need to resort
			return nil, false
		}
		logger.Info().Str("room", update.RoomID()).Msg("room added")
		newlyAdded = true
	}
	if update.EventData.ForceInitial {
		// add room to sub: this applies for when we track all rooms too as we want joins/etc to come through with initial data
		subID := builder.AddSubscription(reqList.RoomSubscription)
		builder.AddRoomsToSubscription(subID, []string{update.RoomID()})
	}
	return s.resort(
		ctx, builder, reqList, intList, update.RoomID(), fromIndex, newlyAdded,
	)
}

// Resort should be called after a specific room has been modified in `intList`.
func (s *connStateLive) resort(
	ctx context.Context, builder *RoomsBuilder,
	reqList *sync3.RequestList, intList *sync3.FilteredSortableRooms, roomID string,
	fromIndex int, newlyAdded bool,
) (ops []sync3.ResponseOp, didUpdate bool) {
	if reqList.ShouldGetAllRooms() {
		// no need to sort this list as we get all rooms
		// no need to calculate ops as we get all rooms
		// no need to send initial state for some rooms as we already sent initial state for all rooms
		if newlyAdded {
			// ensure we send data when the user joins a new room
			subID := builder.AddSubscription(reqList.RoomSubscription)
			builder.AddRoomsToSubscription(subID, []string{roomID})
		}
		return nil, true
	}

	_, wasInsideRange := reqList.Ranges.Inside(int64(fromIndex))
	// this should only move exactly 1 room at most as this is called for every single update
	if err := intList.Sort(reqList.Sort); err != nil {
		logger.Err(err).Msg("cannot sort list")
	}
	toIndex, _ := intList.IndexOf(roomID)
	if newlyAdded {
		wasInsideRange = false // can't be inside the range if this is a new room
	}

	listFromTos, ok := reqList.CalculateMoveIndexes(fromIndex, toIndex)
	if !ok || len(listFromTos) == 0 {
		return nil, false
	}

	for _, listFromTo := range listFromTos {
		listFromIndex := listFromTo[0]
		listToIndex := listFromTo[1]
		wasUpdatedRoomInserted := listToIndex == toIndex
		toRoom := intList.Get(listToIndex)
		roomID = toRoom.RoomID

		// if a different room is being inserted or the room wasn't previously inside a range, send
		// the entire room data
		if !wasUpdatedRoomInserted || !wasInsideRange {
			subID := builder.AddSubscription(reqList.RoomSubscription)
			builder.AddRoomsToSubscription(subID, []string{roomID})
		}

		swapOp := reqList.WriteSwapOp(roomID, listFromIndex, listToIndex)
		ops = append(ops, swapOp...)

		if wasUpdatedRoomInserted && newlyAdded {
			// we inserted this room and it is a new room, so send the entire room data.
			subID := builder.AddSubscription(reqList.RoomSubscription)
			builder.AddRoomsToSubscription(subID, []string{roomID})

			// The client needs to know which item in the list to delete to know which direction to
			// shift items (left or right). This means the vast majority of BRAND NEW rooms will result
			// in a swap op even though you could argue that an INSERT[0] or something is sufficient
			// (though bear in mind that we don't always insert to [0]). The one edge case is when
			// there are no items in the list at all. In this case, there is no swap op as there is
			// nothing to swap, but we still need to tell clients about the operation, hence a lone
			// INSERT op.
			// This can be intuitively explained by saying that "if there is a room at this toIndex
			// then we need a DELETE to tell the client whether the room being replaced should shift
			// left or shift right". In absence of a room being there, we just INSERT, which plays
			// nicely with the logic of "if there is nothing at this index position, insert, else expect
			// a delete op to have happened before".
			if swapOp == nil {
				ops = append(ops, reqList.WriteInsertOp(toIndex, roomID))
			}
		}

	}
	return ops, true
}
