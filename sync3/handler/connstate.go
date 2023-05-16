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
	muxedReq *sync3.Request
	lists    *sync3.InternalRequestLists

	// Confirmed room subscriptions. Entries in this list have been checked for things like
	// "is the user joined to this room?" whereas subscriptions in muxedReq are untrusted.
	roomSubscriptions map[string]sync3.RoomSubscription // room_id -> subscription

	loadPosition int64

	live *connStateLive

	globalCache *caches.GlobalCache
	userCache   *caches.UserCache
	userCacheID int
	lazyCache   *LazyCache

	joinChecker JoinChecker

	extensionsHandler   extensions.HandlerInterface
	processHistogramVec *prometheus.HistogramVec
}

func NewConnState(
	userID, deviceID string, userCache *caches.UserCache, globalCache *caches.GlobalCache,
	ex extensions.HandlerInterface, joinChecker JoinChecker, histVec *prometheus.HistogramVec,
	maxPendingEventUpdates int,
) *ConnState {
	cs := &ConnState{
		globalCache:         globalCache,
		userCache:           userCache,
		userID:              userID,
		deviceID:            deviceID,
		loadPosition:        -1,
		roomSubscriptions:   make(map[string]sync3.RoomSubscription),
		lists:               sync3.NewInternalRequestLists(),
		extensionsHandler:   ex,
		joinChecker:         joinChecker,
		lazyCache:           NewLazyCache(),
		processHistogramVec: histVec,
	}
	cs.live = &connStateLive{
		ConnState:     cs,
		loadPositions: make(map[string]int64),
		updates:       make(chan caches.Update, maxPendingEventUpdates),
	}
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
func (s *ConnState) load(ctx context.Context) error {
	initialLoadPosition, joinedRooms, err := s.globalCache.LoadJoinedRooms(ctx, s.userID)
	if err != nil {
		return err
	}
	rooms := make([]sync3.RoomConnMetadata, len(joinedRooms))
	i := 0
	for _, metadata := range joinedRooms {
		metadata.RemoveHero(s.userID)
		urd := s.userCache.LoadRoomData(metadata.RoomID)
		rooms[i] = sync3.RoomConnMetadata{
			RoomMetadata: *metadata,
			UserRoomData: urd,
		}
		i++
	}
	invites := s.userCache.Invites()
	for _, urd := range invites {
		metadata := urd.Invite.RoomMetadata()
		rooms = append(rooms, sync3.RoomConnMetadata{
			RoomMetadata: *metadata,
			UserRoomData: urd,
		})
	}

	for _, r := range rooms {
		s.lists.SetRoom(r, true)
	}
	s.loadPosition = initialLoadPosition
	return nil
}

// OnIncomingRequest is guaranteed to be called sequentially (it's protected by a mutex in conn.go)
func (s *ConnState) OnIncomingRequest(ctx context.Context, cid sync3.ConnID, req *sync3.Request, isInitial bool) (*sync3.Response, error) {
	if s.loadPosition == -1 {
		// load() needs no ctx so drop it
		_, region := internal.StartSpan(ctx, "load")
		s.load(ctx)
		region.End()
	}
	return s.onIncomingRequest(ctx, req, isInitial)
}

// onIncomingRequest is a callback which fires when the client makes a request to the server. Whilst each request may
// be on their own goroutine, the requests are linearised for us by Conn so it is safe to modify ConnState without
// additional locking mechanisms.
func (s *ConnState) onIncomingRequest(ctx context.Context, req *sync3.Request, isInitial bool) (*sync3.Response, error) {
	start := time.Now()
	// ApplyDelta works fine if s.muxedReq is nil
	var delta *sync3.RequestDelta
	s.muxedReq, delta = s.muxedReq.ApplyDelta(req)
	internal.Logf(ctx, "connstate", "new subs=%v unsubs=%v num_lists=%v", len(delta.Subs), len(delta.Unsubs), len(delta.Lists))
	for key, l := range delta.Lists {
		listData := ""
		if l.Curr != nil {
			listDataBytes, _ := json.Marshal(l.Curr)
			listData = string(listDataBytes)
		}
		internal.Logf(ctx, "connstate", "list[%v] prev_empty=%v curr=%v", key, l.Prev == nil, listData)
	}

	// work out which rooms we'll return data for and add their relevant subscriptions to the builder
	// for it to mix together
	builder := NewRoomsBuilder()
	// works out which rooms are subscribed to but doesn't pull room data
	s.buildRoomSubscriptions(ctx, builder, delta.Subs, delta.Unsubs)
	// works out how rooms get moved about but doesn't pull room data
	respLists := s.buildListSubscriptions(ctx, builder, delta.Lists)

	// pull room data and set changes on the response
	response := &sync3.Response{
		Rooms: s.buildRooms(ctx, builder.BuildSubscriptions()), // pull room data
		Lists: respLists,
	}

	// Handle extensions AFTER processing lists as extensions may need to know which rooms the client
	// is being notified about (e.g. for room account data)
	ctx, region := internal.StartSpan(ctx, "extensions")
	response.Extensions = s.extensionsHandler.Handle(ctx, s.muxedReq.Extensions, extensions.Context{
		UserID:           s.userID,
		DeviceID:         s.deviceID,
		RoomIDToTimeline: response.RoomIDsToTimelineEventIDs(),
		IsInitial:        isInitial,
	})
	region.End()

	if response.ListOps() > 0 || len(response.Rooms) > 0 || response.Extensions.HasData(isInitial) {
		// we're going to immediately return, so track how long this took. We don't do this for long
		// polling requests as high numbers mean nothing. We need to check if we will block as otherwise
		// we will have tons of fast requests logged (as they get tracked and then hit live streaming)
		// In other words, this metric tracks the time it takes to process _changes_ in the client
		// requests (initial connection, modifying index positions, etc) which should always be fast.
		s.trackProcessDuration(time.Since(start), isInitial)
	}

	// do live tracking if we have nothing to tell the client yet
	ctx, region = internal.StartSpan(ctx, "liveUpdate")
	s.live.liveUpdate(ctx, req, s.muxedReq.Extensions, isInitial, response)
	region.End()

	// counts are AFTER events are applied, hence after liveUpdate
	for listKey := range response.Lists {
		l := response.Lists[listKey]
		l.Count = s.lists.Count(listKey)
		response.Lists[listKey] = l
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
			// old rooms use a different subscription
			oldRooms := s.getInitialRoomData(ctx, *bs.RoomSubscription.IncludeOldRooms, oldRoomIDs...)
			for oldRoomID, oldRoom := range oldRooms {
				result[oldRoomID] = oldRoom
			}
		}

		rooms := s.getInitialRoomData(ctx, bs.RoomSubscription, roomIDs...)
		for roomID, room := range rooms {
			result[roomID] = room
		}
	}
	return result
}

func (s *ConnState) getInitialRoomData(ctx context.Context, roomSub sync3.RoomSubscription, roomIDs ...string) map[string]sync3.Room {
	ctx, span := internal.StartSpan(ctx, "getInitialRoomData")
	defer span.End()
	rooms := make(map[string]sync3.Room, len(roomIDs))
	// We want to grab the user room data and the room metadata for each room ID.
	roomIDToUserRoomData := s.userCache.LazyLoadTimelines(ctx, s.loadPosition, roomIDs, int(roomSub.TimelineLimit))
	roomMetadatas := s.globalCache.LoadRooms(ctx, roomIDs...)
	// prepare lazy loading data structures, txn IDs
	roomToUsersInTimeline := make(map[string][]string, len(roomIDToUserRoomData))
	roomToTimeline := make(map[string][]json.RawMessage)
	for roomID, urd := range roomIDToUserRoomData {
		set := make(map[string]struct{})
		for _, ev := range urd.Timeline {
			set[gjson.GetBytes(ev, "sender").Str] = struct{}{}
		}
		userIDs := make([]string, len(set))
		i := 0
		for userID := range set {
			userIDs[i] = userID
			i++
		}
		roomToUsersInTimeline[roomID] = userIDs
		roomToTimeline[roomID] = urd.Timeline
	}
	roomToTimeline = s.userCache.AnnotateWithTransactionIDs(ctx, s.userID, s.deviceID, roomToTimeline)
	rsm := roomSub.RequiredStateMap(s.userID)
	roomIDToState := s.globalCache.LoadRoomState(ctx, roomIDs, s.loadPosition, rsm, roomToUsersInTimeline)
	if roomIDToState == nil { // e.g no required_state
		roomIDToState = make(map[string][]json.RawMessage)
	}
	for _, roomID := range roomIDs {
		userRoomData, ok := roomIDToUserRoomData[roomID]
		if !ok {
			userRoomData = caches.NewUserRoomData()
		}
		metadata := roomMetadatas[roomID]
		var inviteState []json.RawMessage
		// handle invites specially as we do not want to leak additional data beyond the invite_state and if
		// we happen to have this room in the global cache we will do.
		if userRoomData.IsInvite {
			metadata = userRoomData.Invite.RoomMetadata()
			inviteState = userRoomData.Invite.InviteState
		}
		internal.Assert("Metadata is not nil", metadata != nil)
		metadata.RemoveHero(s.userID)
		var requiredState []json.RawMessage
		if !userRoomData.IsInvite {
			requiredState = roomIDToState[roomID]
			if requiredState == nil {
				requiredState = make([]json.RawMessage, 0)
			}
		}
		prevBatch, _ := userRoomData.PrevBatch()
		rooms[roomID] = sync3.Room{
			Name:              internal.CalculateRoomName(metadata, 5), // TODO: customisable?
			NotificationCount: int64(userRoomData.NotificationCount),
			HighlightCount:    int64(userRoomData.HighlightCount),
			Timeline:          roomToTimeline[roomID],
			RequiredState:     requiredState,
			InviteState:       inviteState,
			Initial:           true,
			IsDM:              userRoomData.IsDM,
			JoinedCount:       metadata.JoinCount,
			InvitedCount:      metadata.InviteCount,
			PrevBatch:         prevBatch,
		}
	}

	if rsm.IsLazyLoading() {
		for roomID, userIDs := range roomToUsersInTimeline {
			s.lazyCache.Add(roomID, userIDs...)
		}
	}
	return rooms
}

func (s *ConnState) trackProcessDuration(dur time.Duration, isInitial bool) {
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
}

func (s *ConnState) Alive() bool {
	return !s.live.bufferFull
}

func (s *ConnState) UserID() string {
	return s.userID
}

func (s *ConnState) OnUpdate(ctx context.Context, up caches.Update) {
	s.live.onUpdate(up)
}

// Called by the user cache when updates arrive
func (s *ConnState) OnRoomUpdate(ctx context.Context, up caches.RoomUpdate) {
	switch update := up.(type) {
	case *caches.RoomEventUpdate:
		if update.EventData.LatestPos != caches.PosAlwaysProcess && update.EventData.LatestPos == 0 {
			// 0 -> this event was from a 'state' block, do not poke active connections
			return
		}
		internal.AssertWithContext(ctx, "missing global room metadata", update.GlobalRoomMetadata() != nil)
		internal.Logf(ctx, "connstate", "queued update %d", update.EventData.LatestPos)
		s.live.onUpdate(update)
	case caches.RoomUpdate:
		internal.AssertWithContext(ctx, "missing global room metadata", update.GlobalRoomMetadata() != nil)
		s.live.onUpdate(update)
	default:
		logger.Warn().Str("room_id", up.RoomID()).Msg("OnRoomUpdate unknown update type")
	}
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
