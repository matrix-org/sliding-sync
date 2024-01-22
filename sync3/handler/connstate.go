package handler

import (
	"context"
	"encoding/json"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tidwall/gjson"
)

type JoinChecker interface {
	IsUserJoined(userID, roomID string) bool
}

// ConnState tracks all high-level connection state for this connection, like the combined request
// and the underlying sorted room list. It doesn't track positions of the connection.
type ConnState struct {
	userID   string
	deviceID string
	// the only thing that can touch these data structures is the conn goroutine
	muxedReq        *sync3.Request
	cancelLatestReq context.CancelFunc
	lists           *sync3.InternalRequestLists

	// Confirmed room subscriptions. Entries in this list have been checked for things like
	// "is the user joined to this room?" whereas subscriptions in muxedReq are untrusted.
	roomSubscriptions map[string]sync3.RoomSubscription // room_id -> subscription

	// This is some event NID which is used to anchor any requests for room data from the database
	// to their per-room latest NIDs. It does this by selecting the latest NID for each requested room
	// where the NID is <= this anchor value. Note that there are no ordering guarantees here: it's
	// possible for the anchor to be higher than room X's latest NID and for this connection to have
	// not yet seen room X's latest NID (it'll be sitting in the live buffer). This is why it's important
	// that ConnState DOES NOT ignore events based on this value - it must ignore events based on the real
	// load position for the room.
	// If this value is negative or 0, it means that this connection has not been loaded yet.
	anchorLoadPosition int64
	// roomID -> latest load pos
	loadPositions map[string]int64

	txnIDWaiter *TxnIDWaiter
	live        *connStateLive

	globalCache *caches.GlobalCache
	userCache   *caches.UserCache
	userCacheID int
	lazyCache   *LazyCache

	joinChecker JoinChecker

	extensionsHandler   extensions.HandlerInterface
	setupHistogramVec   *prometheus.HistogramVec
	processHistogramVec *prometheus.HistogramVec
}

func NewConnState(
	userID, deviceID string, userCache *caches.UserCache, globalCache *caches.GlobalCache,
	ex extensions.HandlerInterface, joinChecker JoinChecker, setupHistVec *prometheus.HistogramVec, histVec *prometheus.HistogramVec,
	maxPendingEventUpdates int, maxTransactionIDDelay time.Duration,
) *ConnState {
	cs := &ConnState{
		globalCache:         globalCache,
		userCache:           userCache,
		userID:              userID,
		deviceID:            deviceID,
		anchorLoadPosition:  -1,
		loadPositions:       make(map[string]int64),
		roomSubscriptions:   make(map[string]sync3.RoomSubscription),
		lists:               sync3.NewInternalRequestLists(),
		extensionsHandler:   ex,
		joinChecker:         joinChecker,
		lazyCache:           NewLazyCache(),
		setupHistogramVec:   setupHistVec,
		processHistogramVec: histVec,
	}
	cs.live = &connStateLive{
		ConnState: cs,
		updates:   make(chan caches.Update, maxPendingEventUpdates),
	}
	cs.txnIDWaiter = NewTxnIDWaiter(
		userID,
		maxTransactionIDDelay,
		func(delayed bool, update caches.Update) {
			cs.live.onUpdate(update)
		},
	)
	// subscribe for updates before loading. We risk seeing dupes but that's fine as load positions
	// will stop us double-processing.
	cs.userCacheID = cs.userCache.Subsribe(cs)
	return cs
}

// load the initial joined room list, unfiltered and unsorted, and cache up the fields we care about
// like the room name. We have synchronisation issues here similar to the ConnMap's initial Load.
// However, unlike the ConnMap, we cannot just say "don't start any v2 poll loops yet". To keep things
// synchronised from duplicate event processing, this function will remember the latest NID it used
// to load the initial state, then ignore all incoming events until a syncPosition > the load position
// is received. This guards against the following race condition:
//   - Conn is made. It is atomically added to the ConnMap, making it immediately eligible to be pushed new events.
//   - Between the Conn being added to the ConnMap and the call to load() (done when we get the first HandleIncomingRequest call)
//     N events arrive and get buffered.
//   - load() bases its current state based on the latest position, which includes processing of these N events.
//   - post load() we read N events, processing them a 2nd time.
func (s *ConnState) load(ctx context.Context, req *sync3.Request) error {
	initialLoadPosition, joinedRooms, joinTimings, loadPositions, err := s.globalCache.LoadJoinedRooms(ctx, s.userID)
	if err != nil {
		return err
	}
	for roomID, pos := range loadPositions {
		s.loadPositions[roomID] = pos
	}
	rooms := make([]sync3.RoomConnMetadata, len(joinedRooms))
	i := 0
	for _, metadata := range joinedRooms {
		metadata.RemoveHero(s.userID)
		urd := s.userCache.LoadRoomData(metadata.RoomID)
		timing, ok := joinTimings[metadata.RoomID]
		internal.AssertWithContext(ctx, "LoadJoinedRooms returned room with timing info", ok)
		urd.JoinTiming = timing

		interestedEventTimestampsByList := make(map[string]uint64, len(req.Lists))
		for listKey, listReq := range req.Lists {
			interestingActivityTs := metadata.LastMessageTimestamp
			if len(listReq.BumpEventTypes) > 0 {
				// Use the global cache to find the timestamp of the latest interesting
				// event we can see. If there is no such event, fall back to the
				// LastMessageTimestamp.
				joinEvent := joinTimings[metadata.RoomID]
				interestingActivityTs = joinEvent.Timestamp
				for _, eventType := range listReq.BumpEventTypes {
					timing := metadata.LatestEventsByType[eventType]
					// we found a later event which we are authorised to see, use it instead
					if joinEvent.NID < timing.NID && interestingActivityTs < timing.Timestamp {
						interestingActivityTs = timing.Timestamp
					}
				}
			}
			interestedEventTimestampsByList[listKey] = interestingActivityTs
		}
		rooms[i] = sync3.RoomConnMetadata{
			RoomMetadata:                  *metadata,
			UserRoomData:                  urd,
			LastInterestedEventTimestamps: interestedEventTimestampsByList,
		}
		i++
	}
	invites := s.userCache.Invites()
	for _, urd := range invites {
		metadata := urd.Invite.RoomMetadata()
		inviteTimestampsByList := make(map[string]uint64, len(req.Lists))
		for listKey, _ := range req.Lists {
			inviteTimestampsByList[listKey] = metadata.LastMessageTimestamp
		}
		rooms = append(rooms, sync3.RoomConnMetadata{
			RoomMetadata:                  *metadata,
			UserRoomData:                  urd,
			LastInterestedEventTimestamps: inviteTimestampsByList,
		})
	}

	for _, r := range rooms {
		s.lists.SetRoom(r)
	}
	s.anchorLoadPosition = initialLoadPosition
	return nil
}

// OnIncomingRequest is guaranteed to be called sequentially (it's protected by a mutex in conn.go)
func (s *ConnState) OnIncomingRequest(ctx context.Context, cid sync3.ConnID, req *sync3.Request, isInitial bool, start time.Time) (*sync3.Response, error) {
	if s.anchorLoadPosition <= 0 {
		// load() needs no ctx so drop it
		_, region := internal.StartSpan(ctx, "load")
		err := s.load(ctx, req)
		if err != nil {
			// in practice this means DB hit failures. If we try again later maybe it'll work, and we will because
			// anchorLoadPosition is unset.
			logger.Err(err).Str("conn", cid.String()).Msg("failed to load initial data")
		}
		region.End()
	}
	setupTime := time.Since(start)
	s.trackSetupDuration(ctx, setupTime, isInitial)
	return s.onIncomingRequest(ctx, req, isInitial)
}

// onIncomingRequest is a callback which fires when the client makes a request to the server. Whilst each request may
// be on their own goroutine, the requests are linearised for us by Conn so it is safe to modify ConnState without
// additional locking mechanisms.
func (s *ConnState) onIncomingRequest(reqCtx context.Context, req *sync3.Request, isInitial bool) (*sync3.Response, error) {
	start := time.Now()
	// ApplyDelta works fine if s.muxedReq is nil
	var delta *sync3.RequestDelta
	s.muxedReq, delta = s.muxedReq.ApplyDelta(req)
	internal.Logf(reqCtx, "connstate", "new subs=%v unsubs=%v num_lists=%v", len(delta.Subs), len(delta.Unsubs), len(delta.Lists))
	for key, l := range delta.Lists {
		listData := ""
		if l.Curr != nil {
			listDataBytes, _ := json.Marshal(l.Curr)
			listData = string(listDataBytes)
		}
		internal.Logf(reqCtx, "connstate", "list[%v] prev_empty=%v curr=%v", key, l.Prev == nil, listData)
	}
	for roomID, sub := range s.muxedReq.RoomSubscriptions {
		internal.Logf(reqCtx, "connstate", "room sub[%v] %v", roomID, sub)
	}

	// work out which rooms we'll return data for and add their relevant subscriptions to the builder
	// for it to mix together
	builder := NewRoomsBuilder()
	// works out which rooms are subscribed to but doesn't pull room data
	s.buildRoomSubscriptions(reqCtx, builder, delta.Subs, delta.Unsubs)
	// works out how rooms get moved about but doesn't pull room data
	respLists := s.buildListSubscriptions(reqCtx, builder, delta.Lists)

	// pull room data and set changes on the response
	response := &sync3.Response{
		Rooms: s.buildRooms(reqCtx, builder.BuildSubscriptions()), // pull room data
		Lists: respLists,
	}

	// Handle extensions AFTER processing lists as extensions may need to know which rooms the client
	// is being notified about (e.g. for room account data)
	extCtx, region := internal.StartSpan(reqCtx, "extensions")
	response.Extensions = s.extensionsHandler.Handle(extCtx, s.muxedReq.Extensions, extensions.Context{
		UserID:             s.userID,
		DeviceID:           s.deviceID,
		RoomIDToTimeline:   response.RoomIDsToTimelineEventIDs(),
		IsInitial:          isInitial,
		RoomIDsToLists:     s.lists.ListsByVisibleRoomIDs(s.muxedReq.Lists),
		AllSubscribedRooms: internal.Keys(s.roomSubscriptions),
		AllLists:           s.muxedReq.ListKeys(),
	})
	region.End()

	if response.ListOps() > 0 || len(response.Rooms) > 0 || response.Extensions.HasData(isInitial) {
		// we're going to immediately return, so track how long this took. We don't do this for long
		// polling requests as high numbers mean nothing. We need to check if we will block as otherwise
		// we will have tons of fast requests logged (as they get tracked and then hit live streaming)
		// In other words, this metric tracks the time it takes to process _changes_ in the client
		// requests (initial connection, modifying index positions, etc) which should always be fast.
		s.trackProcessDuration(reqCtx, time.Since(start), isInitial)
	}

	// do live tracking if we have nothing to tell the client yet
	updateCtx, region := internal.StartSpan(reqCtx, "liveUpdate")
	s.live.liveUpdate(updateCtx, req, s.muxedReq.Extensions, isInitial, response)
	region.End()

	// counts are AFTER events are applied, hence after liveUpdate
	for listKey := range response.Lists {
		l := response.Lists[listKey]
		l.Count = s.lists.Count(listKey)
		response.Lists[listKey] = l
	}

	// Add membership events for users sending typing notifications. We do this after live update
	// and initial room loading code so we LL room members in all cases.
	if response.Extensions.Typing != nil && response.Extensions.Typing.HasData(isInitial) {
		s.lazyLoadTypingMembers(reqCtx, response)
	}
	return response, nil
}

func (s *ConnState) onIncomingListRequest(ctx context.Context, builder *RoomsBuilder, listKey string, prevReqList, nextReqList *sync3.RequestList) sync3.ResponseList {
	ctx, span := internal.StartSpan(ctx, "onIncomingListRequest")
	defer span.End()
	roomList, overwritten := s.lists.AssignList(ctx, listKey, nextReqList.Filters, nextReqList.Sort, sync3.DoNotOverwrite)

	if nextReqList.ShouldGetAllRooms() {
		if overwritten || prevReqList.FiltersChanged(nextReqList) {
			// this is either a new list or the filters changed, so we need to splat all the rooms to the client.
			subID := builder.AddSubscription(nextReqList.RoomSubscription)
			allRoomIDs := roomList.RoomIDs()
			builder.AddRoomsToSubscription(ctx, subID, allRoomIDs)
			return sync3.ResponseList{
				// send all the room IDs initially so the user knows which rooms in the top-level rooms map
				// correspond to this list.
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpRange{
						Operation: sync3.OpSync,
						Range:     [2]int64{0, int64(len(allRoomIDs) - 1)},
						RoomIDs:   allRoomIDs,
					},
				},
			}
		}
	}

	// TODO: list deltas
	var responseOperations []sync3.ResponseOp

	var prevRange sync3.SliceRanges
	if prevReqList != nil {
		prevRange = prevReqList.Ranges
	}

	// Handle SYNC / INVALIDATE ranges
	var addedRanges, removedRanges sync3.SliceRanges
	if prevRange != nil {
		addedRanges, removedRanges, _ = prevRange.Delta(nextReqList.Ranges)
	} else {
		addedRanges = nextReqList.Ranges
	}

	sortChanged := prevReqList.SortOrderChanged(nextReqList)
	filtersChanged := prevReqList.FiltersChanged(nextReqList)
	if sortChanged || filtersChanged {
		// the sort/filter operations have changed, invalidate everything (if there were previous syncs), re-sort and re-SYNC
		if prevReqList != nil {
			// there were previous syncs for this list, INVALIDATE the lot
			logger.Trace().Interface("range", prevRange).Msg("INVALIDATEing because sort/filter ops have changed")
			allRoomIDs := roomList.RoomIDs()
			for _, r := range prevRange {
				if r[0] >= roomList.Len() {
					// This range will not have been echoed to the client because it is outside
					// the total length of the room list; do not try to invalidate it.
					continue
				}
				responseOperations = append(responseOperations, &sync3.ResponseOpRange{
					Operation: sync3.OpInvalidate,
					Range:     clampSliceRangeToListSize(ctx, r, int64(len(allRoomIDs))),
				})
			}
		}
		if filtersChanged {
			// we need to re-create the list as the rooms may have completely changed
			roomList, _ = s.lists.AssignList(ctx, listKey, nextReqList.Filters, nextReqList.Sort, sync3.Overwrite)
		}
		// resort as either we changed the sort order or we added/removed a bunch of rooms
		if err := roomList.Sort(nextReqList.Sort); err != nil {
			logger.Err(err).Str("key", listKey).Msg("cannot sort list")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		}
		addedRanges = nextReqList.Ranges
		removedRanges = nil
	}

	// send INVALIDATE for these ranges
	if len(removedRanges) > 0 {
		logger.Trace().Interface("range", removedRanges).Msg("INVALIDATEing because ranges were removed")
	}
	for i := range removedRanges {
		if removedRanges[i][0] >= (roomList.Len()) {
			// This range will not have been echoed to the client because it is outside
			// the total length of the room list; do not try to invalidate it.
			continue
		}
		responseOperations = append(responseOperations, &sync3.ResponseOpRange{
			Operation: sync3.OpInvalidate,
			Range:     clampSliceRangeToListSize(ctx, removedRanges[i], roomList.Len()),
		})
	}

	// inform the builder about this list
	subID := builder.AddSubscription(nextReqList.RoomSubscription)

	// send full room data for these ranges
	for i := range addedRanges {
		sr := sync3.SliceRanges([][2]int64{addedRanges[i]})
		subslice := sr.SliceInto(roomList)
		if len(subslice) == 0 {
			continue
		}
		sortableRooms := subslice[0].(*sync3.SortableRooms)
		roomIDs := sortableRooms.RoomIDs()
		// the builder will populate this with the right room data
		builder.AddRoomsToSubscription(ctx, subID, roomIDs)

		responseOperations = append(responseOperations, &sync3.ResponseOpRange{
			Operation: sync3.OpSync,
			Range:     clampSliceRangeToListSize(ctx, addedRanges[i], roomList.Len()),
			RoomIDs:   roomIDs,
		})
	}

	if prevReqList != nil {
		// If nothing has changed ordering wise in this list (sort/filter) but the timeline limit / req_state has,
		// we need to make a new subscription registering this change to include the new data.
		timelineChanged := prevReqList.TimelineLimitChanged(nextReqList)
		reqStateChanged := prevReqList.RoomSubscription.RequiredStateChanged(nextReqList.RoomSubscription)
		if !sortChanged && !filtersChanged && (timelineChanged || reqStateChanged) {
			var newRS sync3.RoomSubscription
			if timelineChanged {
				newRS.TimelineLimit = nextReqList.TimelineLimit
			}
			if reqStateChanged {
				newRS.RequiredState = nextReqList.RequiredState
			}
			newSubID := builder.AddSubscription(newRS)
			// all the current rooms need to be added to this subscription
			subslice := nextReqList.Ranges.SliceInto(roomList)
			for _, ss := range subslice {
				sortableRooms := ss.(*sync3.SortableRooms)
				roomIDs := sortableRooms.RoomIDs()
				// it's important that we filter out rooms the user is no longer joined to. Specifically,
				// there is a race condition exercised in the security test TestSecurityLiveStreamEventLeftLeak
				// whereby Eve syncs whilst still joined to the room, then she gets kicked, then syncs again
				// with an existing session but with changed req state / timeline params. In this scenario,
				// this code executes BEFORE the kick event has been processed on liveUpdate. This will cause
				// the subscription to fetch the CURRENT state (though not timeline as the load position has
				// not been updated) which Eve should not be able to see as she is no longer joined.
				joinedRoomIDs := make([]string, 0, len(roomIDs))
				for _, roomID := range roomIDs {
					if !s.joinChecker.IsUserJoined(s.userID, roomID) {
						continue
					}
					joinedRoomIDs = append(joinedRoomIDs, roomID)
				}
				// the builder will populate this with the right room data
				builder.AddRoomsToSubscription(ctx, newSubID, joinedRoomIDs)
			}
		}
	}

	return sync3.ResponseList{
		Ops: responseOperations,
		// count will be filled in later
	}
}

func (s *ConnState) buildListSubscriptions(ctx context.Context, builder *RoomsBuilder, listDeltas map[string]sync3.RequestListDelta) map[string]sync3.ResponseList {
	ctx, span := internal.StartSpan(ctx, "buildListSubscriptions")
	defer span.End()
	result := make(map[string]sync3.ResponseList, len(s.muxedReq.Lists))
	// loop each list and handle each independently
	for listKey, list := range listDeltas {
		if list.Curr == nil {
			// they deleted this list
			logger.Debug().Str("key", listKey).Msg("list deleted")
			s.lists.DeleteList(listKey)
			continue
		}
		result[listKey] = s.onIncomingListRequest(ctx, builder, listKey, list.Prev, list.Curr)
	}
	return result
}

func (s *ConnState) buildRoomSubscriptions(ctx context.Context, builder *RoomsBuilder, subs, unsubs []string) {
	ctx, span := internal.StartSpan(ctx, "buildRoomSubscriptions")
	defer span.End()
	for _, roomID := range subs {
		// check that the user is allowed to see these rooms as they can set arbitrary room IDs
		if !s.joinChecker.IsUserJoined(s.userID, roomID) {
			continue
		}

		sub, ok := s.muxedReq.RoomSubscriptions[roomID]
		if !ok {
			logger.Warn().Str("room_id", roomID).Msg(
				"room listed in subscriptions but there is no subscription information in the request, ignoring room subscription.",
			)
			continue
		}
		s.roomSubscriptions[roomID] = sub
		subID := builder.AddSubscription(sub)
		builder.AddRoomsToSubscription(ctx, subID, []string{roomID})
	}
	for _, roomID := range unsubs {
		delete(s.roomSubscriptions, roomID)
	}
}

func (s *ConnState) buildRooms(ctx context.Context, builtSubs []BuiltSubscription) map[string]sync3.Room {
	ctx, span := internal.StartSpan(ctx, "buildRooms")
	defer span.End()
	result := make(map[string]sync3.Room)

	var bumpEventTypes []string
	for _, x := range s.muxedReq.Lists {
		bumpEventTypes = append(bumpEventTypes, x.BumpEventTypes...)
	}

	for _, bs := range builtSubs {
		roomIDs := bs.RoomIDs
		if bs.RoomSubscription.IncludeOldRooms != nil {
			var oldRoomIDs []string
			for _, currRoomID := range bs.RoomIDs { // <- the list of subs we definitely are including
				// append old rooms if we are joined to them
				currRoom := s.lists.ReadOnlyRoom(currRoomID)
				var prevRoomID *string
				if currRoom != nil {
					prevRoomID = currRoom.PredecessorRoomID
				}
				for prevRoomID != nil { // <- the chain of old rooms
					// if not joined, bail
					if !s.joinChecker.IsUserJoined(s.userID, *prevRoomID) {
						break
					}
					oldRoomIDs = append(oldRoomIDs, *prevRoomID)
					// keep checking
					prevRoom := s.lists.ReadOnlyRoom(*prevRoomID)
					if prevRoom != nil {
						prevRoomID = prevRoom.PredecessorRoomID
					}
				}
			}

			// If we have old rooms to fetch, do so.
			if len(oldRoomIDs) > 0 {
				// old rooms use a different subscription
				oldRooms := s.getInitialRoomData(ctx, *bs.RoomSubscription.IncludeOldRooms, bumpEventTypes, oldRoomIDs...)
				for oldRoomID, oldRoom := range oldRooms {
					result[oldRoomID] = oldRoom
				}
			}
		}

		// There won't be anything to fetch, try the next subscription.
		if len(roomIDs) == 0 {
			continue
		}

		rooms := s.getInitialRoomData(ctx, bs.RoomSubscription, bumpEventTypes, roomIDs...)
		for roomID, room := range rooms {
			result[roomID] = room
		}
	}
	return result
}

func (s *ConnState) lazyLoadTypingMembers(ctx context.Context, response *sync3.Response) {
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
			memberEvent := s.globalCache.LoadStateEvent(ctx, roomID, s.loadPositions[roomID], "m.room.member", typingUserID.Str)
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

func (s *ConnState) getInitialRoomData(ctx context.Context, roomSub sync3.RoomSubscription, bumpEventTypes []string, roomIDs ...string) map[string]sync3.Room {
	ctx, span := internal.StartSpan(ctx, "getInitialRoomData")
	defer span.End()

	// 0. Load room metadata and timelines.
	// We want to grab the user room data and the room metadata for each room ID. We use the globally
	// highest NID we've seen to act as an anchor for the request. This anchor does not guarantee that
	// events returned here have already been seen - the position is not globally ordered - so because
	// room A has a position of 6 and B has 7 (so the highest is 7) does not mean that this connection
	// has seen 6, as concurrent room updates cause A and B to race. This is why we then go through the
	// response to this call to assign new load positions for each room.
	roomMetadatas := s.globalCache.LoadRooms(ctx, roomIDs...)
	userRoomDatas := s.userCache.LoadRooms(roomIDs...)
	timelines := s.userCache.LazyLoadTimelines(ctx, s.anchorLoadPosition, roomIDs, int(roomSub.TimelineLimit))

	// 1. Prepare lazy loading data structures, txn IDs.
	roomToUsersInTimeline := make(map[string][]string, len(timelines))
	roomToTimeline := make(map[string][]json.RawMessage)
	for roomID, latestEvents := range timelines {
		senders := make(map[string]struct{})
		for _, ev := range latestEvents.Timeline {
			senders[gjson.GetBytes(ev, "sender").Str] = struct{}{}
		}
		roomToUsersInTimeline[roomID] = internal.Keys(senders)
		roomToTimeline[roomID] = latestEvents.Timeline
		// remember what we just loaded so if we see these events down the live stream we know to ignore them.
		// This means that requesting a direct room subscription causes the connection to jump ahead to whatever
		// is in the database at the time of the call, rather than gradually converging by consuming live data.
		// This is fine, so long as we jump ahead on a per-room basis. We need to make sure (ideally) that the
		// room state is also pinned to the load position here, else you could see weird things in individual
		// responses such as an updated room.name without the associated m.room.name event (though this will
		// come through on the next request -> it converges to the right state so it isn't critical).
		s.loadPositions[roomID] = latestEvents.LatestNID
	}
	roomToTimeline = s.userCache.AnnotateWithTransactionIDs(ctx, s.userID, s.deviceID, roomToTimeline)

	// 2. Load required state events.
	rsm := roomSub.RequiredStateMap(s.userID)
	if rsm.IsLazyLoading() {
		for roomID, userIDs := range roomToUsersInTimeline {
			s.lazyCache.Add(roomID, userIDs...)
		}
	}

	internal.Logf(ctx, "connstate", "getInitialRoomData for %d rooms, RequiredStateMap: %#v", len(roomIDs), rsm)

	// Filter out rooms we are only invited to, as we don't need to fetch the state
	// since we'll be using the invite_state only.
	loadRoomIDs := make([]string, 0, len(roomIDs))
	for _, roomID := range roomIDs {
		userRoomData, ok := userRoomDatas[roomID]
		if !ok || !userRoomData.IsInvite {
			loadRoomIDs = append(loadRoomIDs, roomID)
		}
	}

	// by reusing the same global load position anchor here, we can be sure that the state returned here
	// matches the timeline we loaded earlier - the race conditions happen around pubsub updates and not
	// the events table itself, so whatever position is picked based on this anchor is immutable.
	roomIDToState := s.globalCache.LoadRoomState(ctx, loadRoomIDs, s.anchorLoadPosition, rsm, roomToUsersInTimeline)
	if roomIDToState == nil { // e.g no required_state
		roomIDToState = make(map[string][]json.RawMessage)
	}

	// 3. Build sync3.Room structs to return to clients.
	rooms := make(map[string]sync3.Room, len(roomIDs))
	for _, roomID := range roomIDs {
		userRoomData, ok := userRoomDatas[roomID]
		if !ok {
			userRoomData = caches.NewUserRoomData()
		}
		metadata := roomMetadatas[roomID]
		var inviteState []json.RawMessage
		// handle invites specially as we do not want to leak additional data beyond the invite_state and if
		// we happen to have this room in the global cache we will do.
		// Furthermore, rooms the proxy have been invited to for the first time ever will not be in the global cache yet,
		// which will cause errors below when we try calling functions on a nil metadata.
		if userRoomData.IsInvite {
			metadata = userRoomData.Invite.RoomMetadata()
			inviteState = userRoomData.Invite.InviteState
		}
		metadata.RemoveHero(s.userID)
		var requiredState []json.RawMessage
		if !userRoomData.IsInvite {
			requiredState = roomIDToState[roomID]
			if requiredState == nil {
				requiredState = make([]json.RawMessage, 0)
			}
		}

		// Get the highest timestamp, determined by bumpEventTypes,
		// for this room
		roomListsMeta := s.lists.ReadOnlyRoom(roomID)
		var maxTs uint64
		for _, t := range bumpEventTypes {
			if roomListsMeta == nil {
				break
			}

			evMeta := roomListsMeta.LatestEventsByType[t]
			if evMeta.Timestamp > maxTs {
				maxTs = evMeta.Timestamp
			}
		}

		// If we didn't find any events which would update the timestamp
		// use the join event timestamp instead. Also don't leak
		// timestamp from before we joined.
		if maxTs == 0 || maxTs < roomListsMeta.JoinTiming.Timestamp {
			if roomListsMeta != nil {
				maxTs = roomListsMeta.JoinTiming.Timestamp
				// If no bumpEventTypes are specified, use the
				// LastMessageTimestamp so clients are still able
				// to correctly sort on it.
				if len(bumpEventTypes) == 0 {
					maxTs = roomListsMeta.LastMessageTimestamp
				}
			}
		}

		roomName, calculated := internal.CalculateRoomName(metadata, 5) // TODO: customisable?
		room := sync3.Room{
			Name:              roomName,
			AvatarChange:      sync3.NewAvatarChange(internal.CalculateAvatar(metadata, userRoomData.IsDM)),
			NotificationCount: int64(userRoomData.NotificationCount),
			HighlightCount:    int64(userRoomData.HighlightCount),
			Timeline:          roomToTimeline[roomID],
			RequiredState:     requiredState,
			InviteState:       inviteState,
			Initial:           true,
			IsDM:              userRoomData.IsDM,
			JoinedCount:       metadata.JoinCount,
			InvitedCount:      &metadata.InviteCount,
			PrevBatch:         timelines[roomID].PrevBatch,
			Timestamp:         maxTs,
		}
		if roomSub.IncludeHeroes() && calculated {
			room.Heroes = metadata.Heroes
		}
		rooms[roomID] = room
	}

	return rooms
}

func (s *ConnState) trackSetupDuration(ctx context.Context, dur time.Duration, isInitial bool) {
	internal.SetRequestContextSetupDuration(ctx, dur)
	if s.setupHistogramVec == nil {
		return
	}
	val := "0"
	if isInitial {
		val = "1"
	}
	s.setupHistogramVec.WithLabelValues(val).Observe(float64(dur.Seconds()))
}

func (s *ConnState) trackProcessDuration(ctx context.Context, dur time.Duration, isInitial bool) {
	internal.SetRequestContextProcessingDuration(ctx, dur)
	if s.processHistogramVec == nil {
		return
	}
	val := "0"
	if isInitial {
		val = "1"
	}
	s.processHistogramVec.WithLabelValues(val).Observe(float64(dur.Seconds()))
}

// Called when the connection is torn down
func (s *ConnState) Destroy() {
	s.userCache.Unsubscribe(s.userCacheID)
	logger.Debug().Str("user_id", s.userID).Str("device_id", s.deviceID).Msg("cancelling any in-flight requests")
	if s.cancelLatestReq != nil {
		s.cancelLatestReq()
	}
}

func (s *ConnState) Alive() bool {
	return !s.live.bufferFull
}

func (s *ConnState) UserID() string {
	return s.userID
}

func (s *ConnState) OnUpdate(ctx context.Context, up caches.Update) {
	// will eventually call s.live.onUpdate
	s.txnIDWaiter.Ingest(up)
}

// Called by the user cache when updates arrive
func (s *ConnState) OnRoomUpdate(ctx context.Context, up caches.RoomUpdate) {
	switch update := up.(type) {
	case *caches.RoomEventUpdate:
		if !update.EventData.AlwaysProcess && update.EventData.NID == 0 {
			// 0 -> this event was from a 'state' block, do not poke active connections.
			// This is not the same as checking if we have already processed this event: NID=0 means
			// it's part of initial room state. If we sent these events, we'd send them to clients in
			// the timeline section which is wrong.
			return
		}
		internal.AssertWithContext(ctx, "missing global room metadata", update.GlobalRoomMetadata() != nil)
		internal.Logf(ctx, "connstate", "queued update %d", update.EventData.NID)
		s.OnUpdate(ctx, update)
	case caches.RoomUpdate:
		internal.AssertWithContext(ctx, "missing global room metadata", update.GlobalRoomMetadata() != nil)
		s.OnUpdate(ctx, update)
	default:
		logger.Warn().Str("room_id", up.RoomID()).Msg("OnRoomUpdate unknown update type")
	}
}

func (s *ConnState) PublishEventsUpTo(roomID string, nid int64) {
	s.txnIDWaiter.PublishUpToNID(roomID, nid)
}

func (s *ConnState) SetCancelCallback(cancel context.CancelFunc) {
	s.cancelLatestReq = cancel
}

// clampSliceRangeToListSize helps us to send client-friendly SYNC and INVALIDATE ranges.
//
// Suppose the client asks for a window on positions [10, 19]. If the list
// has exactly 12 rooms, the window will see 3: those at positions 10, 11
// and 12. In this situation, it is helpful for clients to be given the
// "effective" range [10, 12] rather than the "conceptual" range [10, 19].
//
// The "full" room list occupies positions [0, totalRooms - 1]. If the given range r
// does not overlap the full room list, return nil. Otherwise, return the intersection
// of r with the full room list.
func clampSliceRangeToListSize(ctx context.Context, r [2]int64, totalRooms int64) [2]int64 {
	lastIndexWithRoom := totalRooms - 1
	internal.AssertWithContext(ctx, "Start of range exceeds last room index in list", r[0] <= lastIndexWithRoom)
	if r[1] <= lastIndexWithRoom {
		return r
	} else {
		return [2]int64{r[0], lastIndexWithRoom}
	}
}
