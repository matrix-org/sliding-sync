package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/trace"
	"strings"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

// Max number of parameters in a single SQL command
const MaxPostgresParameters = 65535

type Storage struct {
	accumulator      *Accumulator
	EventsTable      *EventTable
	TypingTable      *TypingTable
	ToDeviceTable    *ToDeviceTable
	UnreadTable      *UnreadTable
	AccountDataTable *AccountDataTable
	InvitesTable     *InvitesTable
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
		accumulator:      acc,
		TypingTable:      NewTypingTable(db),
		ToDeviceTable:    NewToDeviceTable(db),
		UnreadTable:      NewUnreadTable(db),
		EventsTable:      acc.eventsTable,
		AccountDataTable: NewAccountDataTable(db),
		InvitesTable:     NewInvitesTable(db),
	}
}

func (s *Storage) LatestEventNID() (int64, error) {
	return s.accumulator.eventsTable.SelectHighestNID()
}

func (s *Storage) LatestTypingID() (int64, error) {
	return s.TypingTable.SelectHighestID()
}

func (s *Storage) AccountData(userID, roomID, eventType string) (data *AccountData, err error) {
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		data, err = s.AccountDataTable.Select(txn, userID, eventType, roomID)
		return err
	})
	return
}

// Pull out all account data for this user. If roomIDs is empty, global account data is returned.
// If roomIDs is non-empty, all account data for these rooms are extracted.
func (s *Storage) AccountDatas(userID string, roomIDs ...string) (datas []AccountData, err error) {
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		datas, err = s.AccountDataTable.SelectMany(txn, userID, roomIDs...)
		return err
	})
	return
}

func (s *Storage) InsertAccountData(userID, roomID string, events []json.RawMessage) (data []AccountData, err error) {
	data = make([]AccountData, len(events))
	for i := range events {
		data[i] = AccountData{
			UserID: userID,
			RoomID: roomID,
			Data:   events[i],
			Type:   gjson.ParseBytes(events[i]).Get("type").Str,
		}
	}
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		data, err = s.AccountDataTable.Insert(txn, data)
		return err
	})
	return data, err
}

// Extract hero info for all rooms. MUST BE CALLED AT STARTUP ONLY AS THIS WILL RACE WITH LIVE TRAFFIC.
func (s *Storage) MetadataForAllRooms() (map[string]internal.RoomMetadata, error) {
	// Select the joined member counts
	// sub-select all current state, filter on m.room.member and then join membership
	rows, err := s.accumulator.db.Query(`
	SELECT room_id, count(state_key) FROM syncv3_events
		WHERE (membership='_join' OR membership = 'join') AND event_type='m.room.member' AND event_nid IN (
			SELECT unnest(events) FROM syncv3_snapshots WHERE syncv3_snapshots.snapshot_id IN (
				SELECT current_snapshot_id FROM syncv3_rooms
			)
		) GROUP BY room_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]internal.RoomMetadata)
	for rows.Next() {
		var metadata internal.RoomMetadata
		if err := rows.Scan(&metadata.RoomID, &metadata.JoinCount); err != nil {
			return nil, err
		}
		result[metadata.RoomID] = metadata
	}
	// Select the invited member counts using the same style of query
	rows, err = s.accumulator.db.Query(`
	SELECT room_id, count(state_key) FROM syncv3_events
		WHERE (membership='_invite' OR membership = 'invite') AND event_type='m.room.member' AND event_nid IN (
			SELECT unnest(events) FROM syncv3_snapshots WHERE syncv3_snapshots.snapshot_id IN (
				SELECT current_snapshot_id FROM syncv3_rooms
			)
		) GROUP BY room_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var roomID string
		var inviteCount int
		if err := rows.Scan(&roomID, &inviteCount); err != nil {
			return nil, err
		}
		metadata := result[roomID]
		metadata.InviteCount = inviteCount
		result[roomID] = metadata
	}

	// work out latest timestamps
	events, err := s.accumulator.eventsTable.selectLatestEventInAllRooms()
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		metadata := result[ev.RoomID]
		metadata.LastMessageTimestamp = gjson.ParseBytes(ev.JSON).Get("origin_server_ts").Uint()
		// it's possible the latest event is a brand new room not caught by the first SELECT for joined
		// rooms e.g when you're invited to a room so we need to make sure to se the metadata again here
		metadata.RoomID = ev.RoomID
		result[ev.RoomID] = metadata
	}

	// Select the name / canonical alias for all rooms
	roomIDToStateEvents, err := s.currentStateEventsInAllRooms([]string{
		"m.room.name", "m.room.canonical_alias",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load state events for all rooms: %s", err)
	}
	for roomID, stateEvents := range roomIDToStateEvents {
		metadata := result[roomID]
		for _, ev := range stateEvents {
			if ev.Type == "m.room.name" && ev.StateKey == "" {
				metadata.NameEvent = gjson.ParseBytes(ev.JSON).Get("content.name").Str
			} else if ev.Type == "m.room.canonical_alias" && ev.StateKey == "" {
				metadata.CanonicalAlias = gjson.ParseBytes(ev.JSON).Get("content.alias").Str
			}
		}
		result[roomID] = metadata
	}

	// Select the most recent members for each room to serve as Heroes. The spec is ambiguous here:
	// "This should be the first 5 members of the room, ordered by stream ordering, which are joined or invited."
	// Unclear if this is the first 5 *most recent* (backwards) or forwards. For now we'll use the most recent
	// ones, and select 6 of them so we can always use 5 no matter who is requesting the room name.
	rows, err = s.accumulator.db.Query(`
	SELECT rf.* FROM (
		SELECT room_id, event, rank() OVER (
			PARTITION BY room_id ORDER BY event_nid DESC
		) FROM syncv3_events WHERE (
			membership='join' OR membership='invite' OR membership='_join'
		) AND event_type='m.room.member' AND event_nid IN (
			SELECT unnest(events) FROM syncv3_snapshots WHERE syncv3_snapshots.snapshot_id IN (
				SELECT current_snapshot_id FROM syncv3_rooms
			)
		)
	) rf WHERE rank <= 6`)
	if err != nil {
		return nil, fmt.Errorf("failed to query heroes: %s", err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var roomID string
		var event json.RawMessage
		var rank int
		if err := rows.Scan(&roomID, &event, &rank); err != nil {
			return nil, err
		}
		ev := gjson.ParseBytes(event)
		targetUser := ev.Get("state_key").Str
		key := roomID + " " + targetUser
		if seen[key] {
			continue
		}
		seen[key] = true
		metadata := result[roomID]
		metadata.Heroes = append(metadata.Heroes, internal.Hero{
			ID:   targetUser,
			Name: ev.Get("content.displayname").Str,
		})
		result[roomID] = metadata
	}

	tx := s.accumulator.db.MustBegin()
	encrypted, err := s.accumulator.roomsTable.SelectEncryptedRooms(tx)
	tx.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to select encrypted rooms: %s", err)
	}
	for _, encryptedRoomID := range encrypted {
		metadata := result[encryptedRoomID]
		metadata.Encrypted = true
		result[encryptedRoomID] = metadata
	}
	tx = s.accumulator.db.MustBegin()
	tombstoned, err := s.accumulator.roomsTable.SelectTombstonedRooms(tx)
	tx.Commit()
	if err != nil {
		return nil, fmt.Errorf("failed to select tombstoned rooms: %s", err)
	}
	for _, tombstonedRoomID := range tombstoned {
		metadata := result[tombstonedRoomID]
		metadata.Tombstoned = true
		result[tombstonedRoomID] = metadata
	}

	return result, nil
}

// Returns all current state events matching the event types given in all rooms. Returns a map of
// room ID to events in that room.
func (s *Storage) currentStateEventsInAllRooms(eventTypes []string) (map[string][]Event, error) {
	query, args, err := sqlx.In(
		`SELECT syncv3_events.room_id, syncv3_events.event_type, syncv3_events.state_key, syncv3_events.event FROM syncv3_events
		WHERE syncv3_events.event_type IN (?)
		AND syncv3_events.event_nid IN (
			SELECT unnest(events) FROM syncv3_snapshots WHERE syncv3_snapshots.snapshot_id IN (SELECT current_snapshot_id FROM syncv3_rooms)
		)`,
		eventTypes,
	)
	if err != nil {
		return nil, err
	}
	rows, err := s.accumulator.db.Query(s.accumulator.db.Rebind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]Event)
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.RoomID, &ev.Type, &ev.StateKey, &ev.JSON); err != nil {
			return nil, err
		}
		result[ev.RoomID] = append(result[ev.RoomID], ev)
	}
	return result, nil
}

func (s *Storage) Accumulate(roomID, prevBatch string, timeline []json.RawMessage) (numNew int, latestNID int64, err error) {
	return s.accumulator.Accumulate(roomID, prevBatch, timeline)
}

func (s *Storage) Initialise(roomID string, state []json.RawMessage) (bool, error) {
	return s.accumulator.Initialise(roomID, state)
}

// Look up room state after the given event position and no further. eventTypesToStateKeys is a map of event type to a list of state keys for that event type.
// If the list of state keys is empty then all events matching that event type will be returned. If the map is empty entirely, then all room state
// will be returned.
func (s *Storage) RoomStateAfterEventPosition(ctx context.Context, roomIDs []string, pos int64, eventTypesToStateKeys map[string][]string) (roomToEvents map[string][]Event, err error) {
	defer trace.StartRegion(ctx, "RoomStateAfterEventPosition").End()
	roomToEvents = make(map[string][]Event, len(roomIDs))
	roomIndex := make(map[string]int, len(roomIDs))
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		// we have 2 ways to pull the latest events:
		//  - superfast rooms table (which races as it can be updated before the new state hits the dispatcher)
		//  - slower events table query
		// we will try to fulfill as many rooms as possible with the rooms table, only using the slower events table
		// query if we can prove we have races. We can prove this because the latest NIDs will be > pos, meaning the
		// database state is ahead of the in-memory state (which is normal as we update the DB first). This should
		// happen infrequently though, so we will warn about this behaviour.
		roomToLatestNIDs, err := s.accumulator.roomsTable.LatestNIDs(txn, roomIDs)
		if err != nil {
			return err
		}
		fastNIDs := make([]int64, 0, len(roomToLatestNIDs))
		var slowRooms []string
		for roomID, latestNID := range roomToLatestNIDs {
			if latestNID > pos {
				slowRooms = append(slowRooms, roomID)
			} else {
				fastNIDs = append(fastNIDs, latestNID)
			}
		}
		latestEvents, err := s.accumulator.eventsTable.SelectByNIDs(txn, true, fastNIDs)
		if err != nil {
			return err
		}
		if len(slowRooms) > 0 {
			logger.Warn().Int("slow_rooms", len(slowRooms)).Msg("RoomStateAfterEventPosition: pos value provided is far behind the database copy, performance degraded")
			latestSlowEvents, err := s.accumulator.eventsTable.LatestEventInRooms(txn, slowRooms, pos)
			if err != nil {
				return err
			}
			latestEvents = append(latestEvents, latestSlowEvents...)
		}
		for i, ev := range latestEvents {
			roomIndex[ev.RoomID] = i
			if ev.BeforeStateSnapshotID == 0 {
				// if there is no before snapshot then this last event NID is _part of_ the initial state,
				// ergo the state after this == the current state and we can safely ignore the lastEventNID
				ev.BeforeStateSnapshotID = 0
				ev.BeforeStateSnapshotID, err = s.accumulator.roomsTable.CurrentAfterSnapshotID(txn, ev.RoomID)
				if err != nil {
					return err
				}
				latestEvents[i] = ev
			}
		}

		if len(eventTypesToStateKeys) == 0 {
			for _, ev := range latestEvents {
				snapshotRow, err := s.accumulator.snapshotTable.Select(txn, ev.BeforeStateSnapshotID)
				if err != nil {
					return err
				}
				// we need to roll forward if this event is state
				if gjson.ParseBytes(ev.JSON).Get("state_key").Exists() {
					if ev.ReplacesNID != 0 {
						// we determined at insert time of this event that this event replaces a nid in the snapshot.
						// find it and replace it
						for j := range snapshotRow.Events {
							if snapshotRow.Events[j] == ev.ReplacesNID {
								snapshotRow.Events[j] = ev.NID
								break
							}
						}
					} else {
						// the event is still state, but it doesn't replace anything, so just add it onto the snapshot
						snapshotRow.Events = append(snapshotRow.Events, ev.NID)
					}
				}
				events, err := s.accumulator.eventsTable.SelectByNIDs(txn, true, snapshotRow.Events)
				if err != nil {
					return err
				}
				roomToEvents[ev.RoomID] = events
			}
		} else {
			// do an optimised query to pull out only the event types and state keys we care about.
			var args []interface{} // event type, state key, event type, state key, ....
			var wheres []string
			for evType, skeys := range eventTypesToStateKeys {
				for _, skey := range skeys {
					args = append(args, evType, skey)
					wheres = append(wheres, "(syncv3_events.event_type = ? AND syncv3_events.state_key = ?)")
				}
				if len(skeys) == 0 {
					args = append(args, evType)
					wheres = append(wheres, "syncv3_events.event_type = ?")
				}
			}
			snapIDs := make([]int64, len(latestEvents))
			for i := range latestEvents {
				snapIDs[i] = latestEvents[i].BeforeStateSnapshotID
			}
			args = append(args, pq.Int64Array(snapIDs))
			// Similar to CurrentStateEventsInAllRooms
			query, args, err := sqlx.In(
				`SELECT syncv3_events.event_nid, syncv3_events.room_id, syncv3_events.event_type, syncv3_events.state_key, syncv3_events.event FROM syncv3_events
				WHERE (`+strings.Join(wheres, " OR ")+`) AND syncv3_events.event_nid IN (
					SELECT unnest(events) FROM syncv3_snapshots WHERE syncv3_snapshots.snapshot_id = ANY(?)
				) ORDER BY syncv3_events.event_nid ASC`,
				args...,
			)
			if err != nil {
				return fmt.Errorf("failed to form sql query: %s", err)
			}
			rows, err := s.accumulator.db.Query(s.accumulator.db.Rebind(query), args...)
			if err != nil {
				return fmt.Errorf("failed to execute query: %s", err)
			}
			defer rows.Close()
			for rows.Next() {
				var ev Event
				if err := rows.Scan(&ev.NID, &ev.RoomID, &ev.Type, &ev.StateKey, &ev.JSON); err != nil {
					return err
				}
				i := roomIndex[ev.RoomID]
				if latestEvents[i].ReplacesNID == ev.NID {
					// this event is replaced by the last event
					ev = latestEvents[i]
				}
				roomToEvents[ev.RoomID] = append(roomToEvents[ev.RoomID], ev)
			}
			// handle the most recent events which won't be in the snapshot but may need to be.
			// we handle the replace case but don't handle brand new state events
			for i := range latestEvents {
				if latestEvents[i].ReplacesNID == 0 {
					// check if we should include it
					for evType, stateKeys := range eventTypesToStateKeys {
						if evType != latestEvents[i].Type {
							continue
						}
						if len(stateKeys) == 0 {
							roomToEvents[latestEvents[i].RoomID] = append(roomToEvents[latestEvents[i].RoomID], latestEvents[i])
						} else {
							for _, skey := range stateKeys {
								if skey == latestEvents[i].StateKey {
									roomToEvents[latestEvents[i].RoomID] = append(roomToEvents[latestEvents[i].RoomID], latestEvents[i])
									break
								}
							}
						}
					}
				}
			}
		}
		return nil
	})
	return
}

func (s *Storage) LatestEventsInRooms(userID string, roomIDs []string, to int64, limit int) (map[string][]json.RawMessage, map[string]string, error) {
	roomIDToRanges, err := s.visibleEventNIDsBetweenForRooms(userID, roomIDs, 0, to)
	if err != nil {
		return nil, nil, err
	}
	result := make(map[string][]json.RawMessage, len(roomIDs))
	prevBatches := make(map[string]string, len(roomIDs))
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		for roomID, ranges := range roomIDToRanges {
			var earliestEventNID int64
			var roomEvents []json.RawMessage
			// start at the most recent range as we want to return the most recent `limit` events
			for i := len(ranges) - 1; i >= 0; i-- {
				if len(roomEvents) >= limit {
					break
				}
				r := ranges[i]
				// the most recent event will be first
				events, err := s.EventsTable.SelectLatestEventsBetween(txn, roomID, r[0]-1, r[1], limit)
				if err != nil {
					return fmt.Errorf("room %s failed to SelectEventsBetween: %s", roomID, err)
				}
				// keep pushing to the front so we end up with A,B,C
				for _, ev := range events {
					roomEvents = append([]json.RawMessage{ev.JSON}, roomEvents...)
					earliestEventNID = ev.NID
					if len(roomEvents) >= limit {
						break
					}
				}
			}
			if earliestEventNID != 0 {
				// the oldest event needs a prev batch token, so find one now
				prevBatch, err := s.EventsTable.SelectClosestPrevBatch(roomID, earliestEventNID)
				if err != nil {
					return fmt.Errorf("failed to select prev_batch for room %s : %s", roomID, err)
				}
				prevBatches[roomID] = prevBatch
			}
			result[roomID] = roomEvents
		}
		return nil
	})
	return result, prevBatches, err
}

func (s *Storage) visibleEventNIDsBetweenForRooms(userID string, roomIDs []string, from, to int64) (map[string][][2]int64, error) {
	// load *THESE* joined rooms for this user at from (inclusive)
	var membershipEvents []Event
	var err error
	if from != 0 {
		// if from==0 then this query will return nothing, so optimise it out
		membershipEvents, err = s.accumulator.eventsTable.SelectEventsWithTypeStateKeyInRooms(roomIDs, "m.room.member", userID, 0, from)
		if err != nil {
			return nil, fmt.Errorf("VisibleEventNIDsBetweenForRooms.SelectEventsWithTypeStateKeyInRooms: %s", err)
		}
	}
	joinedRoomIDs, err := s.joinedRoomsAfterPositionWithEvents(membershipEvents, userID, from)
	if err != nil {
		return nil, fmt.Errorf("failed to work out joined rooms for %s at pos %d: %s", userID, from, err)
	}

	// load membership deltas for *THESE* rooms for this user
	membershipEvents, err = s.accumulator.eventsTable.SelectEventsWithTypeStateKeyInRooms(roomIDs, "m.room.member", userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("failed to load membership events: %s", err)
	}

	return s.visibleEventNIDsWithData(joinedRoomIDs, membershipEvents, userID, from, to)
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
	// load *ALL* joined rooms for this user at from (inclusive)
	joinedRoomIDs, err := s.JoinedRoomsAfterPosition(userID, from)
	if err != nil {
		return nil, fmt.Errorf("failed to work out joined rooms for %s at pos %d: %s", userID, from, err)
	}

	// load *ALL* membership deltas for all rooms for this user
	membershipEvents, err := s.accumulator.eventsTable.SelectEventsWithTypeStateKey("m.room.member", userID, from, to)
	if err != nil {
		return nil, fmt.Errorf("failed to load membership events: %s", err)
	}

	return s.visibleEventNIDsWithData(joinedRoomIDs, membershipEvents, userID, from, to)
}

func (s *Storage) visibleEventNIDsWithData(joinedRoomIDs []string, membershipEvents []Event, userID string, from, to int64) (map[string][][2]int64, error) {
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
	return s.joinedRoomsAfterPositionWithEvents(membershipEvents, userID, pos)
}

func (s *Storage) joinedRoomsAfterPositionWithEvents(membershipEvents []Event, userID string, pos int64) ([]string, error) {
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

func (s *Storage) Teardown() {
	s.accumulator.db.Close()
}
