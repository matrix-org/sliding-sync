package state

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

var log = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

// Accumulator tracks room state and timelines.
//
// In order for it to remain simple(ish), the accumulator DOES NOT SUPPORT arbitrary timeline gaps.
// There is an Initialise function for new rooms (with some pre-determined state) and then a constant
// Accumulate function for timeline events. v2 sync must be called with a large enough timeline.limit
// for this to work!
type Accumulator struct {
	db                    *sqlx.DB
	roomsTable            *RoomsTable
	eventsTable           *EventTable
	snapshotTable         *SnapshotTable
	snapshotRefCountTable *SnapshotRefCountsTable
	membershipLogTable    *MembershipLogTable
	entityName            string
}

func NewAccumulator(db *sqlx.DB) *Accumulator {
	return &Accumulator{
		db:                    db,
		roomsTable:            NewRoomsTable(db),
		eventsTable:           NewEventTable(db),
		snapshotTable:         NewSnapshotsTable(db),
		snapshotRefCountTable: NewSnapshotRefCountsTable(db),
		membershipLogTable:    NewMembershipLogTable(db),
		entityName:            "server",
	}
}

func (a *Accumulator) strippedEventsForSnapshot(txn *sqlx.Tx, snapID int) (StrippedEvents, error) {
	snapshot, err := a.snapshotTable.Select(txn, snapID)
	if err != nil {
		return nil, err
	}
	// pull stripped events as this may be huge (think Matrix HQ)
	return a.eventsTable.SelectStrippedEventsByNIDs(txn, snapshot.Events)
}

// calculateNewSnapshot works out the new snapshot by combining an old and new snapshot. Events get replaced
// if the tuple of event type/state_key match. A new slice is returning (the inputs are not modified)
func (a *Accumulator) calculateNewSnapshot(old StrippedEvents, new StrippedEvents) (StrippedEvents, error) {
	// TODO: implement dendrite's binary tree diff algorithm
	tupleKey := func(e StrippedEvent) string {
		// 0x1f = unit separator
		return e.Type + "\x1f" + e.StateKey
	}
	tupleToNew := make(map[string]StrippedEvent)
	for _, e := range new {
		tupleToNew[tupleKey(e)] = e
	}
	var result StrippedEvents
	for _, e := range old {
		newEvent := tupleToNew[tupleKey(e)]
		if newEvent.NID > 0 {
			result = append(result, StrippedEvent{
				NID:      newEvent.NID,
				Type:     e.Type,
				StateKey: e.StateKey,
			})
			delete(tupleToNew, tupleKey(e))
		} else {
			result = append(result, StrippedEvent{
				NID:      e.NID,
				Type:     e.Type,
				StateKey: e.StateKey,
			})
		}
	}
	// add genuinely new state events from new
	for _, newEvent := range tupleToNew {
		result = append(result, newEvent)
	}
	return result, nil
}

// Initialise starts a new sync accumulator for the given room using the given state as a baseline.
// This will only take effect if this is the first time the v3 server has seen this room, and it wasn't
// possible to get all events up to the create event (e.g Matrix HQ).
//
// This function:
// - Stores these events
// - Sets up the current snapshot based on the state list given.
func (a *Accumulator) Initialise(roomID string, state []json.RawMessage) error {
	if len(state) == 0 {
		return nil
	}
	return sqlutil.WithTransaction(a.db, func(txn *sqlx.Tx) error {
		// Attempt to short-circuit. This has to be done inside a transaction to make sure
		// we don't race with multiple calls to Initialise with the same room ID.
		snapshotID, err := a.roomsTable.CurrentSnapshotID(txn, roomID)
		if err != nil {
			return fmt.Errorf("error fetching snapshot id for room %s: %s", roomID, err)
		}
		if snapshotID > 0 {
			// we only initialise rooms once
			log.Info().Str("room_id", roomID).Int("snapshot_id", snapshotID).Msg("Accumulator.Initialise called but current snapshot already exists, bailing early")
			return nil
		}

		// Insert the events
		events := make([]Event, len(state))
		for i := range events {
			events[i] = Event{
				JSON:   state[i],
				RoomID: roomID,
			}
		}
		numNew, err := a.eventsTable.Insert(txn, events)
		if err != nil {
			return fmt.Errorf("failed to insert events: %w", err)
		}
		if numNew == 0 {
			// we don't have a current snapshot for this room but yet no events are new,
			// no idea how this should be handled.
			log.Error().Str("room_id", roomID).Msg(
				"Accumulator.Initialise: room has no current snapshot but also no new inserted events, doing nothing. This is probably a bug.",
			)
			return nil
		}

		// pull out the event NIDs we just inserted
		eventIDs := make([]string, len(events))
		for i := range eventIDs {
			eventIDs[i] = events[i].ID
		}
		nids, err := a.eventsTable.SelectNIDsByIDs(txn, eventIDs)
		if err != nil {
			return fmt.Errorf("failed to select NIDs for inserted events: %w", err)
		}

		// Make a current snapshot
		snapshot := &SnapshotRow{
			RoomID: roomID,
			Events: pq.Int64Array(nids),
		}
		err = a.snapshotTable.Insert(txn, snapshot)
		if err != nil {
			return fmt.Errorf("failed to insert snapshot: %w", err)
		}

		// Increment the ref counter
		err = a.snapshotRefCountTable.MoveSnapshotRefForEntity(txn, a.entityName, roomID, snapshot.SnapshotID)
		if err != nil {
			return err
		}

		// Set the snapshot ID as the current state
		return a.roomsTable.UpdateCurrentSnapshotID(txn, roomID, snapshot.SnapshotID)
	})
}

// Accumulate internal state from a user's sync response. The timeline order MUST be in the order
// received from the server.
//
// This function does several things:
//   - It ensures all events are persisted in the database. This is shared amongst users.
//   - If all events have been stored before, then it short circuits and returns.
//     This is because we must have already processed this part of the timeline in order for the event
//     to exist in the database, and the sync stream is already linearised for us.
//   - Else it creates a new room state snapshot if the timeline contains state events (as this now represents the current state)
//   - It checks if there are outstanding references for the previous snapshot, and if not, removes the old snapshot from the database.
//     References are made when clients have synced up to a given snapshot (hence may paginate at that point).
//     The server itself also holds a ref to the current state, which is then moved to the new current state.
//   - It adds entries to the membership log for membership events.
func (a *Accumulator) Accumulate(roomID string, timeline []json.RawMessage) error {
	if len(timeline) == 0 {
		return nil
	}
	return sqlutil.WithTransaction(a.db, func(txn *sqlx.Tx) error {
		// Insert the events
		events := make([]Event, len(timeline))
		for i := range events {
			events[i] = Event{
				JSON:   timeline[i],
				RoomID: roomID,
			}
		}
		numNew, err := a.eventsTable.Insert(txn, events)
		if err != nil {
			return err
		}
		if numNew == 0 {
			// nothing to do, we already know about these events
			return nil
		}

		// The last numNew events are new, extract any that are state events
		newEvents := timeline[len(timeline)-numNew:]
		var newStateEvents []json.RawMessage
		var newStateEventIDs []string
		var membershipEventIDs []string
		for _, ev := range newEvents {
			newEvent := gjson.ParseBytes(ev)
			newEventID := newEvent.Get("event_id").Str
			if newEvent.Get("state_key").Exists() {
				newStateEvents = append(newStateEvents, ev)
				newStateEventIDs = append(newStateEventIDs, newEventID)
				if newEvent.Get("type").Str == "m.room.member" {
					// membership event possibly, make sure the membership has changed else
					// things like display name changes will count as membership events :(
					prevMembership := "leave"
					pm := newEvent.Get("unsigned.prev_content.membership")
					if pm.Exists() && pm.Str != "" {
						prevMembership = pm.Str
					}
					currMembership := "leave"
					cm := newEvent.Get("content.membership")
					if cm.Exists() && cm.Str != "" {
						currMembership = cm.Str
					}
					if prevMembership != currMembership { // membership was changed
						membershipEventIDs = append(membershipEventIDs, newEventID)
					}
				}
			}
		}

		// No state events, nothing else to do
		if len(newStateEvents) == 0 {
			return nil
		}

		// State events exist in this timeline, so make a new snapshot
		// by pulling out the current snapshot and adding these state events
		var oldStripped []StrippedEvent
		snapID, err := a.roomsTable.CurrentSnapshotID(txn, roomID)
		if err != nil {
			return err
		}
		if snapID == 0 {
			// a missing snapshot is only okay if this is the start of the room, so we should have a create
			// event in this list somewhere: verify it.
			hasCreateEvent := false
			for _, stateEvent := range newStateEvents {
				if gjson.GetBytes(stateEvent, "type").Str == "m.room.create" {
					hasCreateEvent = true
					break
				}
			}
			if !hasCreateEvent {
				log.Error().Str("room_id", roomID).Msg(
					"Accumulator.Accumulate: room has no current snapshot, and the timeline provided has no create event. " +
						"Either Initialise should be called OR Accumulate with a create event to set up the snapshot. This is a bug.",
				)
				return fmt.Errorf("room not initialised yet!")
			}
		} else {
			oldStripped, err = a.strippedEventsForSnapshot(txn, snapID)
			if err != nil {
				return err
			}
		}
		// pull stripped events for the state we just inserted
		newStripped, err := a.eventsTable.SelectStrippedEventsByIDs(txn, newStateEventIDs)
		if err != nil {
			return err
		}
		currentStripped, err := a.calculateNewSnapshot(oldStripped, newStripped)
		if err != nil {
			return fmt.Errorf("failed to calculate new snapshot: %w", err)
		}
		newSnapshot := &SnapshotRow{
			RoomID: roomID,
			Events: currentStripped.NIDs(),
		}
		if err = a.snapshotTable.Insert(txn, newSnapshot); err != nil {
			return fmt.Errorf("failed to insert new snapshot: %w", err)
		}

		// swap the current snapshot over to this new snapshot and handle ref counters
		if err = a.roomsTable.UpdateCurrentSnapshotID(txn, roomID, newSnapshot.SnapshotID); err != nil {
			return fmt.Errorf("failed to UpdateCurrentSnapshotID to %d: %w", newSnapshot.SnapshotID, err)
		}
		if err = a.snapshotRefCountTable.MoveSnapshotRefForEntity(txn, a.entityName, roomID, newSnapshot.SnapshotID); err != nil {
			return fmt.Errorf("failed to move snapshot ref: %w", err)
		}

		// Add membership logs if this update includes membership changes
		if len(membershipEventIDs) > 0 {
			storedMembershipEvents, err := a.eventsTable.SelectByIDs(txn, membershipEventIDs)
			if err != nil {
				return err
			}
			if len(storedMembershipEvents) != len(membershipEventIDs) {
				return fmt.Errorf("SelectByIDs returned fewer membership events than requested, got %d want %d", len(storedMembershipEvents), len(membershipEventIDs))
			}
			for _, ev := range storedMembershipEvents {
				target := gjson.GetBytes(ev.JSON, "state_key").Str
				err = a.membershipLogTable.AppendMembership(txn, int64(ev.NID), roomID, target)
				if err != nil {
					return fmt.Errorf("AppendMembership failed: %w", err)
				}
			}
		}
		return nil
	})
}

// Delta returns a list of events of at most `limit` for the room not including `lastEventNID`.
// Returns the latest NID of the last event (most recent)
func (a *Accumulator) Delta(roomID string, lastEventNID int64, limit int) (eventsJSON []json.RawMessage, latest int64, err error) {
	txn, err := a.db.Beginx()
	if err != nil {
		return nil, 0, err
	}
	defer txn.Commit()
	events, err := a.eventsTable.SelectEventsBetween(txn, roomID, lastEventNID, EventsEnd, limit)
	if err != nil {
		return nil, 0, err
	}
	if len(events) == 0 {
		return nil, lastEventNID, nil
	}
	eventsJSON = make([]json.RawMessage, len(events))
	for i := range events {
		eventsJSON[i] = events[i].JSON
	}
	return eventsJSON, int64(events[len(events)-1].NID), nil
}
