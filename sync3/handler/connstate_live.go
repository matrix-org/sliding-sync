package handler

import (
	"context"
	"encoding/json"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
)

// the amount of time to try to insert into a full buffer before giving up.
// Customisable for testing
var BufferWaitTime = time.Second * 5

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
	case <-time.After(BufferWaitTime):
		logger.Warn().Interface("update", up).Str("user", s.userID).Str("device", s.deviceID).Msg(
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
	log := logger.With().Str("user", s.userID).Str("device", s.deviceID).Logger()
	// we need to ensure that we keep consuming from the updates channel, even if they want a response
	// immediately. If we have new list data we won't wait, but if we don't then we need to be able to
	// catch-up to the current head position, hence giving 100ms grace period for processing.
	if req.TimeoutMSecs() < 100 {
		req.SetTimeoutMSecs(100)
	}
	startBufferSize := len(s.updates)
	// block until we get a new event, with appropriate timeout
	startTime := time.Now()
	hasLiveStreamed := false
	for response.ListOps() == 0 && len(response.Rooms) == 0 && !response.Extensions.HasData(isInitial) {
		hasLiveStreamed = true
		timeToWait := time.Duration(req.TimeoutMSecs()) * time.Millisecond
		timeWaited := time.Since(startTime)
		timeLeftToWait := timeToWait - timeWaited
		if timeLeftToWait < 0 {
			log.Trace().Str("time_waited", timeWaited.String()).Msg("liveUpdate: timed out")
			return
		}
		log.Trace().Str("dur", timeLeftToWait.String()).Msg("liveUpdate: no response data yet; blocking")
		select {
		case <-ctx.Done(): // client has given up
			log.Trace().Msg("liveUpdate: client gave up")
			internal.Logf(ctx, "liveUpdate", "context cancelled")
			return
		case <-time.After(timeLeftToWait): // we've timed out
			log.Trace().Msg("liveUpdate: timed out")
			internal.Logf(ctx, "liveUpdate", "timed out after %v", timeLeftToWait)
			return
		case update := <-s.updates:
			s.processUpdate(ctx, update, response, ex)
			// if there's more updates and we don't have lots stacked up already, go ahead and process another
			for len(s.updates) > 0 && response.ListOps() < 50 {
				update = <-s.updates
				s.processUpdate(ctx, update, response, ex)
			}
		}
	}

	// If a client constantly changes their request params in every request they make, we will never consume from
	// the update channel as the response will always have data already. In an effort to prevent starvation of new
	// data, we will process some updates even though we have data already, but only if A) we didn't live stream
	// due to natural circumstances, B) it isn't an initial request and C) there is in fact some data there.
	numQueuedUpdates := len(s.updates)
	if !hasLiveStreamed && !isInitial && numQueuedUpdates > 0 {
		for i := 0; i < numQueuedUpdates; i++ {
			update := <-s.updates
			s.processUpdate(ctx, update, response, ex)
		}
		log.Debug().Int("num_queued", numQueuedUpdates).Msg("liveUpdate: caught up")
		internal.Logf(ctx, "connstate", "liveUpdate caught up %d updates", numQueuedUpdates)
	}

	log.Trace().Bool("live_streamed", hasLiveStreamed).Msg("liveUpdate: returning")

	internal.SetConnBufferInfo(ctx, startBufferSize, len(s.updates), cap(s.updates))

	// TODO: op consolidation
}

func (s *connStateLive) processUpdate(ctx context.Context, update caches.Update, response *sync3.Response, ex extensions.Request) {
	internal.Logf(ctx, "liveUpdate", "process live update %s", update.Type())
	s.processLiveUpdate(ctx, update, response)
	// pass event to extensions AFTER processing
	roomIDsToLists := s.lists.ListsByVisibleRoomIDs(s.muxedReq.Lists)
	s.extensionsHandler.HandleLiveUpdate(ctx, update, ex, &response.Extensions, extensions.Context{
		IsInitial:          false,
		RoomIDToTimeline:   response.RoomIDsToTimelineEventIDs(),
		UserID:             s.userID,
		DeviceID:           s.deviceID,
		RoomIDsToLists:     roomIDsToLists,
		AllSubscribedRooms: keys(s.roomSubscriptions),
		AllLists:           s.muxedReq.ListKeys(),
	})
}

func (s *connStateLive) processLiveUpdate(ctx context.Context, up caches.Update, response *sync3.Response) bool {
	internal.AssertWithContext(ctx, "processLiveUpdate: response list length != internal list length", s.lists.Len() == len(response.Lists))
	internal.AssertWithContext(ctx, "processLiveUpdate: request list length != internal list length", s.lists.Len() == len(s.muxedReq.Lists))
	roomUpdate, _ := up.(caches.RoomUpdate)
	roomEventUpdate, _ := up.(*caches.RoomEventUpdate)
	if roomEventUpdate != nil {
		// if this is a room event update we may not want to process this event, for a few reasons.
		if !roomEventUpdate.EventData.AlwaysProcess {
			// check if we should skip this update. Do we know of this room (lp > 0) and if so, is this event
			// behind what we've processed before?
			lp := s.loadPositions[roomEventUpdate.RoomID()]
			if lp > 0 && roomEventUpdate.EventData.NID < lp {
				return false
			}
		}

		// Skip message events from ignored users.
		if roomEventUpdate.EventData.StateKey == nil && s.userCache.ShouldIgnore(roomEventUpdate.EventData.Sender) {
			logger.Trace().
				Str("user", s.userID).
				Str("type", roomEventUpdate.EventData.EventType).
				Str("sender", roomEventUpdate.EventData.Sender).
				Msg("ignoring event update")
			return false
		}
	}

	// for initial rooms e.g a room comes into the window or a subscription now exists
	builder := NewRoomsBuilder()

	// do global connection updates (e.g adding/removing rooms from allRooms)
	delta := s.processGlobalUpdates(ctx, builder, up)

	// process room subscriptions
	hasUpdates := s.processUpdatesForSubscriptions(ctx, builder, up)

	// do per-list updates (e.g resorting, adding/removing rooms which no longer match filter)
	for _, listDelta := range delta.Lists {
		listKey := listDelta.ListKey
		list := s.lists.Get(listKey)
		reqList := s.muxedReq.Lists[listKey]
		resList := response.Lists[listKey]
		updates := s.processLiveUpdateForList(ctx, builder, up, listDelta.Op, &reqList, list, &resList)
		if updates {
			hasUpdates = true
		}
		response.Lists[listKey] = resList
	}

	// add in initial rooms FIRST as we replace whatever is in the rooms key for these rooms.
	// If we do it after appending live updates then we can lose updates because we replace what
	// we accumulated.
	rooms := s.buildRooms(ctx, builder.BuildSubscriptions())
	for roomID, room := range rooms {
		response.Rooms[roomID] = room
	}

	// TODO: find a better way to determine if the triggering event should be included e.g ask the lists?
	if hasUpdates && roomEventUpdate != nil {
		// include this update in the rooms response TODO: filters on event type?
		userRoomData := roomUpdate.UserRoomMetadata()
		r := response.Rooms[roomUpdate.RoomID()]

		// Get the highest timestamp, determined by bumpEventTypes,
		// for this room
		roomListsMeta := s.lists.ReadOnlyRoom(roomUpdate.RoomID())
		var bumpEventTypes []string
		for _, list := range s.muxedReq.Lists {
			bumpEventTypes = append(bumpEventTypes, list.BumpEventTypes...)
		}
		for _, t := range bumpEventTypes {
			evMeta := roomListsMeta.LatestEventsByType[t]
			if evMeta.Timestamp > r.Timestamp {
				r.Timestamp = evMeta.Timestamp
			}
		}

		// If there are no bumpEventTypes defined, use the last message timestamp
		if r.Timestamp == 0 && len(bumpEventTypes) == 0 {
			r.Timestamp = roomUpdate.GlobalRoomMetadata().LastMessageTimestamp
		}
		// Make sure we don't leak a timestamp from before we joined
		if r.Timestamp < roomListsMeta.JoinTiming.Timestamp {
			r.Timestamp = roomListsMeta.JoinTiming.Timestamp
		}

		r.HighlightCount = int64(userRoomData.HighlightCount)
		r.NotificationCount = int64(userRoomData.NotificationCount)
		if roomEventUpdate != nil && roomEventUpdate.EventData.Event != nil {
			r.NumLive++
			advancedPastEvent := false
			if !roomEventUpdate.EventData.AlwaysProcess {
				if roomEventUpdate.EventData.NID <= s.loadPositions[roomEventUpdate.RoomID()] {
					// this update has been accounted for by the initial:true room snapshot
					advancedPastEvent = true
				}
				s.loadPositions[roomEventUpdate.RoomID()] = roomEventUpdate.EventData.NID
			}
			// we only append to the timeline if we haven't already got this event. This can happen when:
			// - 2 live events for a room mid-connection
			// - next request bumps a room from outside to inside the window
			// - the initial:true room from BuildSubscriptions contains the latest live events in the timeline as it's pulled from the DB
			// - we then process the live events in turn which adds them again.
			if !advancedPastEvent {
				roomIDtoTimeline := s.userCache.AnnotateWithTransactionIDs(ctx, s.userID, s.deviceID, map[string][]json.RawMessage{
					roomEventUpdate.RoomID(): {roomEventUpdate.EventData.Event},
				})
				r.Timeline = append(r.Timeline, roomIDtoTimeline[roomEventUpdate.RoomID()]...)
				roomID := roomEventUpdate.RoomID()
				sender := roomEventUpdate.EventData.Sender
				if s.lazyCache.IsLazyLoading(roomID) && !s.lazyCache.IsSet(roomID, sender) {
					// load the state event
					memberEvent := s.globalCache.LoadStateEvent(context.Background(), roomID, s.loadPositions[roomID], "m.room.member", sender)
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
				roomName, calculated := internal.CalculateRoomName(metadata, 5) // TODO: customisable?

				thisRoom.Name = roomName

				if calculated && s.shouldIncludeHeroes(roomUpdate.RoomID()) {
					thisRoom.Heroes = metadata.Heroes
				}
			}
			if delta.RoomAvatarChanged {
				metadata := roomUpdate.GlobalRoomMetadata()
				metadata.RemoveHero(s.userID)
				thisRoom.AvatarChange = sync3.NewAvatarChange(internal.CalculateAvatar(metadata))
			}
			if delta.InviteCountChanged {
				thisRoom.InvitedCount = &roomUpdate.GlobalRoomMetadata().InviteCount
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
	roomEventUpdate, isRoomEventUpdate := up.(*caches.RoomEventUpdate)

	bumpTimestampInList := make(map[string]uint64, len(s.muxedReq.Lists))
	rup, isRoomUpdate := up.(caches.RoomUpdate)
	if isRoomUpdate {
		updateTimestamp := rup.GlobalRoomMetadata().LastMessageTimestamp
		for listKey, list := range s.muxedReq.Lists {
			if len(list.BumpEventTypes) == 0 {
				// If this list hasn't provided BumpEventTypes, bump the room list for all room updates.
				bumpTimestampInList[listKey] = updateTimestamp
			} else if isRoomEventUpdate {
				// If BumpEventTypes are provided, only bump the room if we see an event
				// matching one of the bump types. We don't consult rup.JoinTiming here,
				// because we should only be processing events that the user is
				// permitted to see.
				for _, eventType := range list.BumpEventTypes {
					if eventType == roomEventUpdate.EventData.EventType {
						bumpTimestampInList[listKey] = updateTimestamp
						break
					}
				}
			}
		}

		metadata := rup.GlobalRoomMetadata().DeepCopy()
		metadata.RemoveHero(s.userID)
		// TODO: if we change a room from being a DM to not being a DM, we should call
		// SetRoom and recalculate avatars. To do that we'd need to
		//  - listen to m.direct global account data events
		//   - compute the symmetric difference between old and new
		//   - call SetRooms for each room in the difference.
		// I'm assuming this happens so rarely that we can ignore this for now. PRs
		// welcome if you a strong opinion to the contrary.
		delta = s.lists.SetRoom(sync3.RoomConnMetadata{
			RoomMetadata:                  *metadata,
			UserRoomData:                  *rup.UserRoomMetadata(),
			LastInterestedEventTimestamps: bumpTimestampInList,
		})
	}

	// update the anchor for this new event
	if isRoomEventUpdate && roomEventUpdate.EventData.NID > s.anchorLoadPosition {
		s.anchorLoadPosition = roomEventUpdate.EventData.NID
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
			builder.AddRoomsToSubscription(ctx, subID, []string{update.RoomID()})
		}
	case *caches.UnreadCountUpdate:
		logger.Trace().Str("user", s.userID).Str("room", update.RoomID()).Bool("count_decreased", update.HasCountDecreased).Msg("received unread count update")
		// normally we do not signal unread count increases to the client as we want to atomically
		// increase the count AND send the msg so there's no phantom msgs/notifications. However,
		// we must resort the list and send delta even if this is an increase else
		// the server's view of the world can get out-of-sync with the clients, causing bogus DELETE/INSERT
		// ops which causes duplicate rooms to appear.
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
			builder.AddRoomsToSubscription(ctx, subID, []string{roomID})
		} else if listOp == sync3.ListOpDel {
			intList.Remove(roomID)
		}
		return nil, true
	}

	ops, subs := sync3.CalculateListOps(ctx, reqList, intList, roomID, listOp)
	if len(subs) > 0 { // handle rooms which have just come into the window
		subID := builder.AddSubscription(reqList.RoomSubscription)
		builder.AddRoomsToSubscription(ctx, subID, subs)
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

// shouldIncludeHeroes returns whether the given roomID is in a list or direct
// subscription which should return heroes.
func (s *connStateLive) shouldIncludeHeroes(roomID string) bool {
	if s.roomSubscriptions[roomID].IncludeHeroes() {
		return true
	}
	roomIDsToLists := s.lists.ListsByVisibleRoomIDs(s.muxedReq.Lists)
	for _, listKey := range roomIDsToLists[roomID] {
		// check if this list should include heroes
		if !s.muxedReq.Lists[listKey].IncludeHeroes() {
			continue
		}
		return true
	}
	return false
}
