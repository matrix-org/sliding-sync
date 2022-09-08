package handler

import (
	"context"
	"encoding/json"
	"runtime/trace"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/caches"
	"github.com/matrix-org/sync-v3/sync3/extensions"
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

	joinChecker JoinChecker

	extensionsHandler extensions.HandlerInterface
}

func NewConnState(
	userID, deviceID string, userCache *caches.UserCache, globalCache *caches.GlobalCache,
	ex extensions.HandlerInterface, joinChecker JoinChecker,
) *ConnState {
	cs := &ConnState{
		globalCache:       globalCache,
		userCache:         userCache,
		userID:            userID,
		deviceID:          deviceID,
		roomSubscriptions: make(map[string]sync3.RoomSubscription),
		lists:             sync3.NewInternalRequestLists(),
		extensionsHandler: ex,
		joinChecker:       joinChecker,
	}
	cs.live = &connStateLive{
		ConnState: cs,
		updates:   make(chan caches.Update, MaxPendingEventUpdates), // TODO: customisable
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
func (s *ConnState) load() error {
	initialLoadPosition, joinedRooms, err := s.globalCache.LoadJoinedRooms(s.userID)
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
		s.lists.SetRoom(r)
	}
	s.loadPosition = initialLoadPosition
	return nil
}

// OnIncomingRequest is guaranteed to be called sequentially (it's protected by a mutex in conn.go)
func (s *ConnState) OnIncomingRequest(ctx context.Context, cid sync3.ConnID, req *sync3.Request, isInitial bool) (*sync3.Response, error) {
	ctx, task := trace.NewTask(ctx, "OnIncomingRequest")
	defer task.End()
	if s.loadPosition == 0 {
		region := trace.StartRegion(ctx, "load")
		s.load()
		region.End()
	}
	return s.onIncomingRequest(ctx, req, isInitial)
}

// onIncomingRequest is a callback which fires when the client makes a request to the server. Whilst each request may
// be on their own goroutine, the requests are linearised for us by Conn so it is safe to modify ConnState without
// additional locking mechanisms.
func (s *ConnState) onIncomingRequest(ctx context.Context, req *sync3.Request, isInitial bool) (*sync3.Response, error) {
	// ApplyDelta works fine if s.muxedReq is nil
	var delta *sync3.RequestDelta
	s.muxedReq, delta = s.muxedReq.ApplyDelta(req)

	// associate extensions context
	ex := s.muxedReq.Extensions
	ex.UserID = s.userID
	ex.DeviceID = s.deviceID

	// work out which rooms we'll return data for and add their relevant subscriptions to the builder
	// for it to mix together
	builder := NewRoomsBuilder()
	// works out which rooms are subscribed to but doesn't pull room data
	s.buildRoomSubscriptions(builder, delta.Subs, delta.Unsubs)
	// works out how rooms get moved about but doesn't pull room data
	respLists := s.buildListSubscriptions(ctx, builder, delta.Lists)

	// pull room data and set changes on the response
	response := &sync3.Response{
		Rooms: s.buildRooms(ctx, builder.BuildSubscriptions()), // pull room data
		Lists: respLists,
	}

	includedRoomIDs := make(map[string]struct{})
	for roomID := range response.Rooms {
		includedRoomIDs[roomID] = struct{}{}
	}
	// Handle extensions AFTER processing lists as extensions may need to know which rooms the client
	// is being notified about (e.g. for room account data)
	region := trace.StartRegion(ctx, "extensions")
	response.Extensions = s.extensionsHandler.Handle(ex, includedRoomIDs, isInitial)
	region.End()

	// do live tracking if we have nothing to tell the client yet
	region = trace.StartRegion(ctx, "liveUpdate")
	s.live.liveUpdate(ctx, req, ex, isInitial, response)
	region.End()

	// counts are AFTER events are applied, hence after liveUpdate
	for i := range response.Lists {
		response.Lists[i].Count = s.lists.Count(i)
	}

	return response, nil
}

func (s *ConnState) onIncomingListRequest(ctx context.Context, builder *RoomsBuilder, listIndex int, prevReqList, nextReqList *sync3.RequestList) sync3.ResponseList {
	defer trace.StartRegion(ctx, "onIncomingListRequest").End()
	roomList, overwritten := s.lists.AssignList(listIndex, nextReqList.Filters, nextReqList.Sort, sync3.DoNotOverwrite)

	if nextReqList.ShouldGetAllRooms() {
		if overwritten || prevReqList.FiltersChanged(nextReqList) {
			// this is either a new list or the filters changed, so we need to splat all the rooms to the client.
			subID := builder.AddSubscription(nextReqList.RoomSubscription)
			allRoomIDs := roomList.RoomIDs()
			builder.AddRoomsToSubscription(subID, allRoomIDs)
			return sync3.ResponseList{
				// send all the room IDs initially so the user knows which rooms in the top-level rooms map
				// correspond to this list.
				Ops: []sync3.ResponseOp{
					&sync3.ResponseOpRange{
						Operation: sync3.OpSync,
						Range:     []int64{0, int64(len(allRoomIDs) - 1)},
						RoomIDs:   allRoomIDs,
					},
				},
			}
		}
	}

	// TODO: calculate the M values for N < M calcs
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
			for _, r := range prevRange {
				responseOperations = append(responseOperations, &sync3.ResponseOpRange{
					Operation: sync3.OpInvalidate,
					Range:     r[:],
				})
			}
		}
		if filtersChanged {
			// we need to re-create the list as the rooms may have completely changed
			roomList, _ = s.lists.AssignList(listIndex, nextReqList.Filters, nextReqList.Sort, sync3.Overwrite)
		}
		// resort as either we changed the sort order or we added/removed a bunch of rooms
		if err := roomList.Sort(nextReqList.Sort); err != nil {
			logger.Err(err).Int("index", listIndex).Msg("cannot sort list")
		}
		addedRanges = nextReqList.Ranges
		removedRanges = nil
	}

	// send INVALIDATE for these ranges
	if len(removedRanges) > 0 {
		logger.Trace().Interface("range", removedRanges).Msg("INVALIDATEing because ranges were removed")
	}
	for _, r := range removedRanges {
		responseOperations = append(responseOperations, &sync3.ResponseOpRange{
			Operation: sync3.OpInvalidate,
			Range:     r[:],
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
		builder.AddRoomsToSubscription(subID, roomIDs)

		responseOperations = append(responseOperations, &sync3.ResponseOpRange{
			Operation: sync3.OpSync,
			Range:     addedRanges[i][:],
			RoomIDs:   roomIDs,
		})
	}

	return sync3.ResponseList{
		Ops: responseOperations,
		// count will be filled in later
	}
}

func (s *ConnState) buildListSubscriptions(ctx context.Context, builder *RoomsBuilder, listDeltas []sync3.RequestListDelta) []sync3.ResponseList {
	result := make([]sync3.ResponseList, len(s.muxedReq.Lists))
	// loop each list and handle each independently
	for i := range listDeltas {
		if listDeltas[i].Curr == nil {
			// they deleted this list
			logger.Debug().Int("index", i).Msg("list deleted")
			s.lists.DeleteList(i)
			continue
		}
		result[i] = s.onIncomingListRequest(ctx, builder, i, listDeltas[i].Prev, listDeltas[i].Curr)
	}
	return result
}

func (s *ConnState) buildRoomSubscriptions(builder *RoomsBuilder, subs, unsubs []string) {
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
		builder.AddRoomsToSubscription(subID, []string{roomID})
	}
	for _, roomID := range unsubs {
		delete(s.roomSubscriptions, roomID)
	}
}

func (s *ConnState) buildRooms(ctx context.Context, builtSubs []BuiltSubscription) map[string]sync3.Room {
	defer trace.StartRegion(ctx, "buildRooms").End()
	result := make(map[string]sync3.Room)
	for _, bs := range builtSubs {
		rooms := s.getInitialRoomData(ctx, bs.RoomSubscription, bs.RoomIDs...)
		for roomID, room := range rooms {
			result[roomID] = room
		}
	}
	return result
}

func (s *ConnState) getInitialRoomData(ctx context.Context, roomSub sync3.RoomSubscription, roomIDs ...string) map[string]sync3.Room {
	rooms := make(map[string]sync3.Room, len(roomIDs))
	// We want to grab the user room data and the room metadata for each room ID.
	roomIDToUserRoomData := s.userCache.LazyLoadTimelines(s.loadPosition, roomIDs, int(roomSub.TimelineLimit))
	roomMetadatas := s.globalCache.LoadRooms(roomIDs...)
	roomIDToState := s.globalCache.LoadRoomState(ctx, roomIDs, s.loadPosition, roomSub.RequiredStateMap())

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
		metadata.RemoveHero(s.userID)
		var requiredState []json.RawMessage
		if !userRoomData.IsInvite {
			requiredState = roomIDToState[roomID]
		}
		prevBatch, _ := userRoomData.PrevBatch()
		rooms[roomID] = sync3.Room{
			Name:              internal.CalculateRoomName(metadata, 5), // TODO: customisable?
			NotificationCount: int64(userRoomData.NotificationCount),
			HighlightCount:    int64(userRoomData.HighlightCount),
			Timeline:          s.userCache.AnnotateWithTransactionIDs(userRoomData.Timeline),
			RequiredState:     requiredState,
			InviteState:       inviteState,
			Initial:           true,
			IsDM:              userRoomData.IsDM,
			JoinedCount:       metadata.JoinCount,
			InvitedCount:      metadata.InviteCount,
			PrevBatch:         prevBatch,
		}
	}
	return rooms
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

func (s *ConnState) OnUpdate(up caches.Update) {
	s.live.onUpdate(up)
}

// Called by the user cache when updates arrive
func (s *ConnState) OnRoomUpdate(up caches.RoomUpdate) {
	switch update := up.(type) {
	case *caches.RoomEventUpdate:
		if update.EventData.LatestPos != caches.PosAlwaysProcess {
			if update.EventData.LatestPos == 0 || update.EventData.LatestPos < s.loadPosition {
				// 0 -> this event was from a 'state' block, do not poke active connections
				// pos < load -> this event has already been processed from the initial load, do not poke active connections
				return
			}
		}
		internal.Assert("missing global room metadata", update.GlobalRoomMetadata() != nil)
		s.live.onUpdate(update)
	case caches.RoomUpdate:
		internal.Assert("missing global room metadata", update.GlobalRoomMetadata() != nil)
		s.live.onUpdate(update)
	default:
		logger.Warn().Str("room_id", up.RoomID()).Msg("OnRoomUpdate unknown update type")
	}
}
