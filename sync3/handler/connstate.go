package handler

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/extensions"
)

var (
	// The max number of events the client is eligible to read (unfiltered) which we are willing to
	// buffer on this connection. Too large and we consume lots of memory. Too small and busy accounts
	// will trip the connection knifing.
	MaxPendingEventUpdates = 200
)

type ConnEvent struct {
	roomMetadata *internal.RoomMetadata
	roomID       string
	msg          *sync3.EventData
	userMsg      struct {
		msg               *sync3.UserRoomData
		hasCountDecreased bool
	}
}

// ConnState tracks all high-level connection state for this connection, like the combined request
// and the underlying sorted room list. It doesn't track session IDs or positions of the connection.
type ConnState struct {
	userID string
	// the only thing that can touch these data structures is the conn goroutine
	muxedReq          *sync3.Request
	lists             *sync3.SortableRoomLists
	roomSubscriptions map[string]sync3.RoomSubscription
	loadPosition      int64

	// A channel which the dispatcher uses to send updates to the conn goroutine
	// Consumed when the conn is read. There is a limit to how many updates we will store before
	// saying the client is dead and clean up the conn.
	updateEvents chan *ConnEvent

	globalCache *sync3.GlobalCache
	userCache   *sync3.UserCache
	userCacheID int
	bufferFull  bool

	extensionsHandler extensions.HandlerInterface
}

func NewConnState(userID string, userCache *sync3.UserCache, globalCache *sync3.GlobalCache, ex extensions.HandlerInterface) *ConnState {
	cs := &ConnState{
		globalCache:       globalCache,
		userCache:         userCache,
		userID:            userID,
		roomSubscriptions: make(map[string]sync3.RoomSubscription),
		updateEvents:      make(chan *ConnEvent, MaxPendingEventUpdates), // TODO: customisable
		lists:             &sync3.SortableRoomLists{},
		extensionsHandler: ex,
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
func (s *ConnState) load(req *sync3.Request) error {
	initialLoadPosition, joinedRooms, err := s.globalCache.LoadJoinedRooms(s.userID)
	if err != nil {
		return err
	}
	rooms := make([]sync3.RoomConnMetadata, len(joinedRooms))
	for i := range joinedRooms {
		metadata := joinedRooms[i]
		metadata.RemoveHero(s.userID)
		urd := s.userCache.LoadRoomData(metadata.RoomID)
		rooms[i] = sync3.RoomConnMetadata{
			RoomMetadata: *metadata,
			UserRoomData: urd,
			CanonicalisedName: strings.ToLower(
				strings.Trim(internal.CalculateRoomName(metadata, 5), "#!()):_@"),
			),
		}
	}
	s.loadPosition = initialLoadPosition

	for i, l := range req.Lists {
		roomList := sync3.NewFilteredSortableRooms(rooms, l.Filters)
		sortBy := []string{sync3.SortByRecency}
		if l.Sort != nil {
			sortBy = l.Sort
		}
		err := roomList.Sort(sortBy)
		if err != nil {
			logger.Warn().Err(err).Strs("sort", sortBy).Msg("failed to sort")
		}
		s.lists.Set(i, roomList)
	}
	return nil
}

// OnIncomingRequest is guaranteed to be called sequentially (it's protected by a mutex in conn.go)
func (s *ConnState) OnIncomingRequest(ctx context.Context, cid sync3.ConnID, req *sync3.Request) (*sync3.Response, error) {
	if s.loadPosition == 0 {
		s.load(req)
	}
	return s.onIncomingRequest(ctx, req)
}

// onIncomingRequest is a callback which fires when the client makes a request to the server. Whilst each request may
// be on their own goroutine, the requests are linearised for us by Conn so it is safe to modify ConnState without
// additional locking mechanisms.
func (s *ConnState) onIncomingRequest(ctx context.Context, req *sync3.Request) (*sync3.Response, error) {
	prevReq := s.muxedReq

	// TODO: factor out room subscription handling
	var newSubs []string
	var newUnsubs []string
	if s.muxedReq == nil {
		s.muxedReq = req
		for roomID := range req.RoomSubscriptions {
			newSubs = append(newSubs, roomID)
		}
	} else {
		combinedReq, subs, unsubs := s.muxedReq.ApplyDelta(req)
		s.muxedReq = combinedReq
		newSubs = subs
		newUnsubs = unsubs
	}
	// start forming the response, handle subscriptions
	response := &sync3.Response{
		RoomSubscriptions: s.updateRoomSubscriptions(int(sync3.DefaultTimelineLimit), newSubs, newUnsubs),
		Extensions:        s.extensionsHandler.Handle(s.muxedReq.Extensions),
	}
	responseOperations := []sync3.ResponseOp{} // empty not nil slice

	// loop each list and handle each independently
	for i := range s.muxedReq.Lists {
		var prevList *sync3.RequestList
		if prevReq != nil && i < len(prevReq.Lists) {
			prevList = &prevReq.Lists[i]
		}
		ops := s.onIncomingListRequest(i, prevList, &s.muxedReq.Lists[i])
		responseOperations = append(responseOperations, ops...)
	}

	// do live tracking if we have nothing to tell the client yet
	// block until we get a new event, with appropriate timeout
blockloop:
	for len(responseOperations) == 0 && len(response.RoomSubscriptions) == 0 && !response.Extensions.HasData() {
		select {
		case <-ctx.Done(): // client has given up
			break blockloop
		case <-time.After(time.Duration(req.TimeoutSecs()) * time.Second):
			break blockloop
		case connEvent := <-s.updateEvents: // TODO: keep reading until it is empty before responding.
			if connEvent.roomMetadata != nil {
				// always update our view of the world
				s.lists.ForEach(func(index int, list *sync3.FilteredSortableRooms) {
					// TODO: yuck that the index is here
					deletedIndex := list.UpdateGlobalRoomMetadata(connEvent.roomMetadata)
					if deletedIndex >= 0 && s.muxedReq.Lists[index].Rooms.Inside(int64(deletedIndex)) {
						responseOperations = append(responseOperations, &sync3.ResponseOpSingle{
							List:      index,
							Operation: sync3.OpDelete,
							Index:     &deletedIndex,
						})
					}
				})
			}
			if connEvent.msg != nil {
				subs, ops := s.processIncomingEvent(connEvent.msg)
				responseOperations = append(responseOperations, ops...)
				for _, sub := range subs {
					response.RoomSubscriptions[sub.RoomID] = sub
				}
			}
			if connEvent.userMsg.msg != nil {
				subs, ops := s.processIncomingUserEvent(connEvent.roomID, connEvent.userMsg.msg, connEvent.userMsg.hasCountDecreased)
				responseOperations = append(responseOperations, ops...)
				for _, sub := range subs {
					response.RoomSubscriptions[sub.RoomID] = sub
				}
			}
		}
	}

	response.Ops = responseOperations
	response.Counts = s.lists.Counts() // counts are AFTER events are applied

	return response, nil
}

func (s *ConnState) onIncomingListRequest(listIndex int, prevReqList, nextReqList *sync3.RequestList) []sync3.ResponseOp {
	roomList := s.lists.List(listIndex)
	// TODO: calculate the M values for N < M calcs
	// TODO: list deltas
	var responseOperations []sync3.ResponseOp

	var prevRange sync3.SliceRanges
	var prevSort []string
	if prevReqList != nil {
		prevRange = prevReqList.Rooms
		prevSort = prevReqList.Sort
	}
	if nextReqList.Sort == nil {
		nextReqList.Sort = []string{sync3.SortByRecency}
	}

	// Handle SYNC / INVALIDATE ranges

	var addedRanges, removedRanges sync3.SliceRanges
	if prevRange != nil {
		addedRanges, removedRanges, _ = prevRange.Delta(nextReqList.Rooms)
	} else {
		addedRanges = nextReqList.Rooms
	}
	if !reflect.DeepEqual(prevSort, nextReqList.Sort) {
		// the sort operations have changed, invalidate everything (if there were previous syncs), re-sort and re-SYNC
		if prevSort != nil {
			for _, r := range prevReqList.Rooms {
				responseOperations = append(responseOperations, &sync3.ResponseOpRange{
					List:      listIndex,
					Operation: sync3.OpInvalidate,
					Range:     r[:],
				})
			}
		}
		if err := roomList.Sort(nextReqList.Sort); err != nil {
			logger.Err(err).Int("index", listIndex).Msg("cannot sort list")
		}
		addedRanges = nextReqList.Rooms
		removedRanges = nil
	}

	// send INVALIDATE for these ranges
	for _, r := range removedRanges {
		responseOperations = append(responseOperations, &sync3.ResponseOpRange{
			List:      listIndex,
			Operation: sync3.OpInvalidate,
			Range:     r[:],
		})
	}
	// send full room data for these ranges
	for _, r := range addedRanges {
		sr := sync3.SliceRanges([][2]int64{r})
		subslice := sr.SliceInto(roomList)
		if len(subslice) == 0 {
			continue
		}
		sortableRooms := subslice[0].(*sync3.SortableRooms)
		roomIDs := sortableRooms.RoomIDs()

		responseOperations = append(responseOperations, &sync3.ResponseOpRange{
			List:      listIndex,
			Operation: sync3.OpSync,
			Range:     r[:],
			Rooms:     s.getInitialRoomData(int(nextReqList.TimelineLimit), roomIDs...),
		})
	}

	return responseOperations
}

func (s *ConnState) processIncomingUserEvent(roomID string, userEvent *sync3.UserRoomData, hasCountDecreased bool) ([]sync3.Room, []sync3.ResponseOp) {
	var responseOperations []sync3.ResponseOp
	var rooms []sync3.Room

	s.lists.ForEach(func(index int, list *sync3.FilteredSortableRooms) {
		// modify notification counts
		deletedIndex := list.UpdateUserRoomMetadata(roomID, userEvent, hasCountDecreased)
		// only notify if we are tracking this index
		if deletedIndex >= 0 && s.muxedReq.Lists[index].Rooms.Inside(int64(deletedIndex)) {
			responseOperations = append(responseOperations, &sync3.ResponseOpSingle{
				List:      index,
				Operation: sync3.OpDelete,
				Index:     &deletedIndex,
			})
		}
	})

	if !hasCountDecreased {
		// if the count increases then we'll notify the user for the event which increases the count, hence
		// do nothing. We only care to notify the user when the counts decrease.
		return nil, nil
	}

	s.lists.ForEach(func(index int, list *sync3.FilteredSortableRooms) {
		fromIndex, ok := list.IndexOf(roomID)
		if !ok {
			return
		}
		roomSubs, ops := s.resort(index, &s.muxedReq.Lists[index], list, roomID, fromIndex, nil)
		rooms = append(rooms, roomSubs...)
		responseOperations = append(responseOperations, ops...)
	})
	return rooms, responseOperations
}

func (s *ConnState) processIncomingEvent(updateEvent *sync3.EventData) ([]sync3.Room, []sync3.ResponseOp) {
	var responseOperations []sync3.ResponseOp
	var rooms []sync3.Room

	// keep track of the latest stream position
	if updateEvent.LatestPos > s.loadPosition {
		s.loadPosition = updateEvent.LatestPos
	}

	s.lists.ForEach(func(index int, list *sync3.FilteredSortableRooms) {

		fromIndex, ok := list.IndexOf(updateEvent.RoomID)
		if !ok {
			// the user may have just joined the room hence not have an entry in this list yet.
			fromIndex = int(list.Len())
			roomMetadatas := s.globalCache.LoadRooms(updateEvent.RoomID)
			roomMetadata := roomMetadatas[0]
			roomMetadata.RemoveHero(s.userID)
			newRoomConn := sync3.RoomConnMetadata{
				RoomMetadata: *roomMetadata,
				UserRoomData: s.userCache.LoadRoomData(updateEvent.RoomID),
				CanonicalisedName: strings.ToLower(
					strings.Trim(internal.CalculateRoomName(roomMetadata, 5), "#!()):_@"),
				),
			}
			if !list.Add(newRoomConn) {
				// we didn't add this room to the list so we don't need to resort
				return
			}
		}
		roomSubs, ops := s.resort(index, &s.muxedReq.Lists[index], list, updateEvent.RoomID, fromIndex, updateEvent.Event)
		rooms = append(rooms, roomSubs...)
		responseOperations = append(responseOperations, ops...)
	})
	return rooms, responseOperations
}

// Resort should be called after a specific room has been modified in `sortedJoinedRooms`.
func (s *ConnState) resort(listIndex int, reqList *sync3.RequestList, roomList *sync3.FilteredSortableRooms, roomID string, fromIndex int, newEvent json.RawMessage) ([]sync3.Room, []sync3.ResponseOp) {
	if reqList.Sort == nil {
		reqList.Sort = []string{sync3.SortByRecency}
	}
	if err := roomList.Sort(reqList.Sort); err != nil {
		logger.Err(err).Msg("cannot sort list")
	}
	var subs []sync3.Room

	isSubscribedToRoom := false
	if _, ok := s.roomSubscriptions[roomID]; ok {
		// there is a subscription for this room, so update the room subscription field
		subs = append(subs, *s.getDeltaRoomData(roomID, newEvent))
		isSubscribedToRoom = true
	}
	toIndex, _ := roomList.IndexOf(roomID)
	logger = logger.With().Str("room", roomID).Int("from", fromIndex).Int("to", toIndex).Logger()
	logger.Info().Msg("moved!")
	// the toIndex may not be inside a tracked range. If it isn't, we actually need to notify about a
	// different room
	if !reqList.Rooms.Inside(int64(toIndex)) {
		logger.Info().Msg("room isn't inside tracked range")
		toIndex = int(reqList.Rooms.UpperClamp(int64(toIndex)))
		count := int(roomList.Len())
		if toIndex >= count {
			// no room exists
			logger.Warn().Int("to", toIndex).Int("size", count).Msg(
				"cannot move to index, it's greater than the list of sorted rooms",
			)
			return subs, nil
		}
		if toIndex == -1 {
			logger.Warn().Int("from", fromIndex).Int("to", toIndex).Interface("ranges", reqList.Rooms).Msg(
				"room moved but not in tracked ranges, ignoring",
			)
			return subs, nil
		}
		toRoom := roomList.Get(toIndex)

		// fake an update event for this room.
		// We do this because we are introducing a new room in the list because of this situation:
		// tracking [10,20] and room 24 jumps to position 0, so now we are tracking [9,19] as all rooms
		// have been shifted to the right
		rooms := s.userCache.LazyLoadTimelines(s.loadPosition, []string{toRoom.RoomID}, int(reqList.TimelineLimit)) // TODO: per-room timeline limit
		urd := rooms[toRoom.RoomID]

		// clobber before falling through
		roomID = toRoom.RoomID
		newEvent = urd.Timeline[len(urd.Timeline)-1]
	}

	return subs, s.moveRoom(reqList, listIndex, roomID, newEvent, fromIndex, toIndex, reqList.Rooms, isSubscribedToRoom)
}

func (s *ConnState) updateRoomSubscriptions(timelineLimit int, subs, unsubs []string) map[string]sync3.Room {
	result := make(map[string]sync3.Room)
	for _, roomID := range subs {
		sub, ok := s.muxedReq.RoomSubscriptions[roomID]
		if !ok {
			logger.Warn().Str("room_id", roomID).Msg(
				"room listed in subscriptions but there is no subscription information in the request, ignoring room subscription.",
			)
			continue
		}
		s.roomSubscriptions[roomID] = sub
		// send initial room information
		if sub.TimelineLimit > 0 {
			timelineLimit = int(sub.TimelineLimit)
		}
		rooms := s.getInitialRoomData(timelineLimit, roomID)
		result[roomID] = rooms[0]
	}
	for _, roomID := range unsubs {
		delete(s.roomSubscriptions, roomID)
	}
	return result
}

func (s *ConnState) getDeltaRoomData(roomID string, event json.RawMessage) *sync3.Room {
	userRoomData := s.userCache.LoadRoomData(roomID)
	room := &sync3.Room{
		RoomID:            roomID,
		NotificationCount: int64(userRoomData.NotificationCount),
		HighlightCount:    int64(userRoomData.HighlightCount),
	}
	if event != nil {
		room.Timeline = []json.RawMessage{
			event,
		}
	}
	return room
}

func (s *ConnState) getInitialRoomData(timelineLimit int, roomIDs ...string) []sync3.Room {
	roomIDToUserRoomData := s.userCache.LazyLoadTimelines(s.loadPosition, roomIDs, timelineLimit) // TODO: per-room timeline limit
	rooms := make([]sync3.Room, len(roomIDs))
	roomMetadatas := s.globalCache.LoadRooms(roomIDs...)
	for i, roomID := range roomIDs {
		userRoomData := roomIDToUserRoomData[roomID]
		metadata := roomMetadatas[i]
		metadata.RemoveHero(s.userID)

		rooms[i] = sync3.Room{
			RoomID:            roomID,
			Name:              internal.CalculateRoomName(metadata, 5), // TODO: customisable?
			NotificationCount: int64(userRoomData.NotificationCount),
			HighlightCount:    int64(userRoomData.HighlightCount),
			Timeline:          userRoomData.Timeline,
			RequiredState:     s.globalCache.LoadRoomState(roomID, s.loadPosition, s.muxedReq.GetRequiredState(0, roomID)),
		}
	}
	return rooms
}

// Called when the user cache has a new event for us. This callback fires when the server gets a new event and determines this connection MAY be
// interested in it (e.g the client is joined to the room or it's an invite, etc). Each callback can fire
// from different v2 poll loops, and there is no locking in order to prevent a slow ConnState from wedging the poll loop.
// We need to move this data onto a channel for onIncomingRequest to consume later.
func (s *ConnState) onNewConnectionEvent(connEvent *ConnEvent) {
	if s.bufferFull {
		return
	}
	eventData := connEvent.msg
	// TODO: remove 0 check when Initialise state returns sensible positions
	if eventData != nil && eventData.LatestPos != 0 && eventData.LatestPos < s.loadPosition {
		// do not push this event down the stream as we have already processed it when we loaded
		// the room list initially.
		return
	}
	select {
	case s.updateEvents <- connEvent:
	case <-time.After(5 * time.Second):
		// TODO: kill the connection
		logger.Warn().Interface("event", *connEvent).Str("user", s.userID).Msg(
			"cannot send event to connection, buffer exceeded. Destroying connection.",
		)
		s.bufferFull = true
		s.Destroy()
	}
}

// Called when the connection is torn down
func (s *ConnState) Destroy() {
	s.userCache.Unsubscribe(s.userCacheID)
}

func (s *ConnState) Alive() bool {
	return !s.bufferFull
}

func (s *ConnState) UserID() string {
	return s.userID
}

// Move a room from an absolute index position to another absolute position.
// 1,2,3,4,5
// 3 bumps to top -> 3,1,2,4,5 -> DELETE index=2, INSERT val=3 index=0
// 7 bumps to top -> 7,1,2,3,4 -> DELETE index=4, INSERT val=7 index=0
func (s *ConnState) moveRoom(reqList *sync3.RequestList, listIndex int, roomID string, event json.RawMessage, fromIndex, toIndex int, ranges sync3.SliceRanges, onlySendRoomID bool) []sync3.ResponseOp {
	if fromIndex == toIndex {
		// issue an UPDATE, nice and easy because we don't need to move entries in the list
		room := &sync3.Room{
			RoomID: roomID,
		}
		if !onlySendRoomID {
			room = s.getDeltaRoomData(roomID, event)
		}
		return []sync3.ResponseOp{
			&sync3.ResponseOpSingle{
				List:      listIndex,
				Operation: sync3.OpUpdate,
				Index:     &fromIndex,
				Room:      room,
			},
		}
	}
	// work out which value to DELETE. This varies depending on where the room was and how much of the
	// list we are tracking. E.g moving to index=0 with ranges [0,99][100,199] and an update in
	// pos 150 -> DELETE 150, but if we weren't tracking [100,199] then we would DELETE 99. If we were
	// tracking [0,99][200,299] then it's still DELETE 99 as the 200-299 range isn't touched.
	deleteIndex := fromIndex
	if !ranges.Inside(int64(fromIndex)) {
		// we are not tracking this room, so no point issuing a DELETE for it. Instead, clamp the index
		// to the highest end-range marker < index
		deleteIndex = int(ranges.LowerClamp(int64(fromIndex)))
	}
	room := &sync3.Room{
		RoomID: roomID,
	}
	if !onlySendRoomID {
		rooms := s.getInitialRoomData(int(reqList.TimelineLimit), roomID)
		room = &rooms[0]
	}
	return []sync3.ResponseOp{
		&sync3.ResponseOpSingle{
			List:      listIndex,
			Operation: sync3.OpDelete,
			Index:     &deleteIndex,
		},
		&sync3.ResponseOpSingle{
			List:      listIndex,
			Operation: sync3.OpInsert,
			Index:     &toIndex,
			Room:      room,
		},
	}

}

// Called by the user cache when events arrive
func (s *ConnState) OnNewEvent(event *sync3.EventData) {
	// pull the current room metadata from the global cache. This is safe to do without locking
	// as the v2 poll loops all rely on a single poller thread to poke the dispatcher which pokes
	// the caches (incl. user caches) so there cannot be any concurrent updates. We always get back
	// a copy of the metadata from LoadRoom so we can pass pointers around freely.
	meta := s.globalCache.LoadRooms(event.RoomID)
	s.onNewConnectionEvent(&ConnEvent{
		roomMetadata: meta[0],
		roomID:       event.RoomID,
		msg:          event,
	})
}

// Called by the user cache when unread counts have changed
func (s *ConnState) OnUnreadCountsChanged(userID, roomID string, urd sync3.UserRoomData, hasCountDecreased bool) {
	var ce ConnEvent
	ce.roomID = roomID
	ce.userMsg.hasCountDecreased = hasCountDecreased
	ce.userMsg.msg = &urd
	s.onNewConnectionEvent(&ce)
}
