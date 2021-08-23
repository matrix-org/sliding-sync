package state

import (
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/tidwall/gjson"
)

type Storage struct {
	accumulator   *Accumulator
	TypingTable   *TypingTable
	ToDeviceTable *ToDeviceTable
}

func NewStorage(postgresURI string) *Storage {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	acc := &Accumulator{
		db:            db,
		roomsTable:    NewRoomsTable(db),
		eventsTable:   NewEventTable(db),
		snapshotTable: NewSnapshotsTable(db),
		entityName:    "server",
	}
	return &Storage{
		accumulator:   acc,
		TypingTable:   NewTypingTable(db),
		ToDeviceTable: NewToDeviceTable(db),
	}
}

func (s *Storage) LatestEventNID() (int64, error) {
	return s.accumulator.eventsTable.SelectHighestNID()
}

func (s *Storage) LatestTypingID() (int64, error) {
	return s.TypingTable.SelectHighestID()
}

func (s *Storage) Accumulate(roomID string, timeline []json.RawMessage) (int, error) {
	return s.accumulator.Accumulate(roomID, timeline)
}

func (s *Storage) Initialise(roomID string, state []json.RawMessage) (bool, error) {
	return s.accumulator.Initialise(roomID, state)
}

func (s *Storage) RoomStateAfterEventPosition(roomID string, pos int64) (events []Event, err error) {
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		lastEventNID, replacesNID, snapID, err := s.accumulator.eventsTable.BeforeStateSnapshotIDForEventNID(txn, roomID, pos)
		if err != nil {
			return err
		}
		currEventIsState := false
		if lastEventNID > 0 {
			// now load the event which has this before_snapshot and see if we need to roll it forward
			lastEvents, err := s.accumulator.eventsTable.SelectByNIDs(txn, true, []int64{lastEventNID})
			if err != nil {
				return fmt.Errorf("SelectByNIDs last event nid %d : %s", lastEventNID, err)
			}
			lastEvent := gjson.ParseBytes(lastEvents[0].JSON)
			currEventIsState = lastEvent.Get("state_key").Exists()
		}
		snapshotRow, err := s.accumulator.snapshotTable.Select(txn, snapID)
		if err != nil {
			return err
		}
		// we need to roll forward if this event is state
		if currEventIsState {
			if replacesNID != 0 {
				// we determined at insert time of this event that this event replaces a nid in the snapshot.
				// find it and replace it
				for i := range snapshotRow.Events {
					if snapshotRow.Events[i] == replacesNID {
						snapshotRow.Events[i] = lastEventNID
						break
					}
				}
			} else {
				// the event is still state, but it doesn't replace anything, so just add it onto the snapshot
				snapshotRow.Events = append(snapshotRow.Events, lastEventNID)
			}
		}
		events, err = s.accumulator.eventsTable.SelectByNIDs(txn, true, snapshotRow.Events)
		if err != nil {
			return err
		}

		return err
	})
	return
}

func (s *Storage) RoomStateBeforeEventPosition(roomID string, pos int64) (events []Event, err error) {
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		_, _, snapID, err := s.accumulator.eventsTable.BeforeStateSnapshotIDForEventNID(txn, roomID, pos)
		if err != nil {
			return err
		}
		snapshotRow, err := s.accumulator.snapshotTable.Select(txn, snapID)
		if err != nil {
			return err
		}
		events, err = s.accumulator.eventsTable.SelectByNIDs(txn, true, snapshotRow.Events)
		return err
	})
	return
}

// Work out the NID ranges to pull events from for this user. Given a from and to event nid stream position,
// this function returns a map of room ID to a slice of 2-element from|to positions. These positions are
// all INCLUSIVE, and the client should be informed of these events at some point. For example:
//
//                     Stream Positions
//           1     2   3    4   5   6   7   8   9   10
//   Room A  Maj   E   E                E
//   Room B                 E   Maj E
//   Room C                                 E   Mal E   (a already joined to this room at position 0)
//
//   E=message event, M=membership event, followed by user letter, followed by 'i' or 'j' or 'l' for invite|join|leave
//
//   - For Room A: from=1, to=10, returns { RoomA: [ [1,10] ]}  (tests events in joined room)
//   - For Room B: from=1, to=10, returns { RoomB: [ [5,10] ]}  (tests joining a room starts events)
//   - For Room C: from=1, to=10, returns { RoomC: [ [0,9] ]}  (tests leaving a room stops events)
//
// Multiple slices can occur when a user leaves and re-joins the same room, and invites are same-element positions:
//
//                     Stream Positions
//           1     2   3    4   5   6   7   8   9   10  11  12  13  14  15
//   Room D  Maj                E   Mal E   Maj E   Mal E
//   Room E        E   Mai  E                               E   Maj E   E
//
//  - For Room D: from=1, to=15 returns { RoomD: [ [1,6], [8,10] ] } (tests multi-join/leave)
//  - For Room E: from=1, to=15 returns { RoomE: [ [3,3], [13,15] ] } (tests invites)
func (s *Storage) VisibleEventNIDsBetween(userID string, from, to int64) (map[string][][2]int64, error) {
	membershipEvents, err := s.accumulator.eventsTable.SelectEventsWithTypeStateKey("m.room.member", userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("failed to load membership events: %s", err)
	}
	// load membership events in order and bucket based on room ID
	roomIDToLogs := make(map[string][]membershipEvent)
	for _, ev := range membershipEvents {
		evJSON := gjson.ParseBytes(ev.JSON)
		roomIDToLogs[ev.RoomID] = append(roomIDToLogs[ev.RoomID], membershipEvent{
			Event:      ev,
			StateKey:   evJSON.Get("state_key").Str,
			Membership: evJSON.Get("content.membership").Str,
		})
	}

	// load all joined rooms for this user at from (inclusive)
	joinedRoomIDs, err := s.JoinedRoomsAfterPosition(userID, from)
	if err != nil {
		return nil, fmt.Errorf("failed to work out joined rooms for %s at pos %d: %s", userID, from, err)
	}
	fmt.Printf("JoinedRoomsAfterPosition from=%d -> %v\n", from, joinedRoomIDs)

	// Performs the algorithm
	calculateVisibleEventNIDs := func(isJoined bool, fromIncl, toIncl int64, logs []membershipEvent) [][2]int64 {
		// short circuit when there are no membership deltas
		if len(logs) == 0 {
			return [][2]int64{
				{
					fromIncl, toIncl,
				},
			}
		}
		var result [][2]int64
		var startIndex int64 = -1
		if isJoined {
			startIndex = fromIncl
		}
		for _, memEvent := range logs {
			// check for a valid transition (join->leave|ban or leave|invite->join) - we won't always get valid transitions
			// e.g logs will be there for things like leave->ban which we don't care about
			isValidTransition := false
			if isJoined && (memEvent.Membership == "leave" || memEvent.Membership == "ban") {
				isValidTransition = true
			} else if !isJoined && memEvent.Membership == "join" {
				isValidTransition = true
			} else if !isJoined && memEvent.Membership == "invite" {
				// short-circuit: invites are sent on their own and don't affect ranges
				result = append(result, [2]int64{memEvent.NID, memEvent.NID})
				continue
			}
			if !isValidTransition {
				continue
			}
			if isJoined {
				// transitioning to leave, we get all events up to and including the leave event
				result = append(result, [2]int64{startIndex, memEvent.NID})
				isJoined = false
			} else {
				// transitioning to joined, we will get the join and some more events in a bit
				startIndex = memEvent.NID
				isJoined = true
			}
		}
		// if we are still joined to the room at this point, grab all events up to toIncl
		if isJoined {
			result = append(result, [2]int64{startIndex, toIncl})
		}
		return result
	}

	// For each joined room, perform the algorithm and delete the logs afterwards
	result := make(map[string][][2]int64)
	for _, joinedRoomID := range joinedRoomIDs {
		roomResult := calculateVisibleEventNIDs(true, from, to, roomIDToLogs[joinedRoomID])
		result[joinedRoomID] = roomResult
		delete(roomIDToLogs, joinedRoomID)
	}

	// Handle rooms which we are not joined to but have logs for
	for roomID, logs := range roomIDToLogs {
		roomResult := calculateVisibleEventNIDs(false, from, to, logs)
		result[roomID] = roomResult
	}

	return result, nil
}

func (s *Storage) RoomMembershipDelta(roomID string, from, to int64, limit int) (eventJSON []json.RawMessage, upTo int64, err error) {
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		nids, err := s.accumulator.eventsTable.SelectEventNIDsWithTypeInRoom(txn, "m.room.member", limit, roomID, from, to)
		if err != nil {
			return err
		}
		if len(nids) == 0 {
			return nil
		}
		upTo = nids[len(nids)-1]
		events, err := s.accumulator.eventsTable.SelectByNIDs(txn, true, nids)
		if err != nil {
			return err
		}
		eventJSON = make([]json.RawMessage, len(events))
		for i := range events {
			eventJSON[i] = events[i].JSON
		}
		return nil
	})
	return
}

func (s *Storage) AllJoinedMembers() (map[string][]string, error) {
	roomIDToEventNIDs, err := s.accumulator.snapshotTable.CurrentSnapshots()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string)
	for roomID, eventNIDs := range roomIDToEventNIDs {
		events, err := s.accumulator.eventsTable.SelectByNIDs(nil, true, eventNIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to select events in room %s: %s", roomID, err)
		}
		for _, ev := range events {
			evj := gjson.ParseBytes(ev.JSON)
			if evj.Get("type").Str != gomatrixserverlib.MRoomMember {
				continue
			}
			if evj.Get("content.membership").Str != gomatrixserverlib.Join {
				continue
			}
			result[roomID] = append(result[roomID], evj.Get("state_key").Str)
		}
	}
	return result, nil
}

func (s *Storage) JoinedRoomsAfterPosition(userID string, pos int64) ([]string, error) {
	// fetch all the membership events up to and including pos
	membershipEvents, err := s.accumulator.eventsTable.SelectEventsWithTypeStateKey("m.room.member", userID, 0, pos)
	if err != nil {
		return nil, fmt.Errorf("JoinedRoomsAfterPosition.SelectEventsWithTypeStateKey: %s", err)
	}
	joinedRoomsSet := make(map[string]bool)
	for _, ev := range membershipEvents {
		// some of these events will be profile changes but that's ok as we're just interested in the
		// end result, not the deltas
		membership := gjson.GetBytes(ev.JSON, "content.membership").Str
		switch membership {
		case "join":
			joinedRoomsSet[ev.RoomID] = true
		case "ban":
			fallthrough
		case "leave":
			joinedRoomsSet[ev.RoomID] = false
		}
	}
	joinedRooms := make([]string, 0, len(joinedRoomsSet))
	for roomID, joined := range joinedRoomsSet {
		if joined {
			joinedRooms = append(joinedRooms, roomID)
		}
	}

	return joinedRooms, nil
}
