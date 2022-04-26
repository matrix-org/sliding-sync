package state

import (
	"database/sql"
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
	db            *sqlx.DB
	roomsTable    *RoomsTable
	eventsTable   *EventTable
	snapshotTable *SnapshotTable
	entityName    string
}

func NewAccumulator(db *sqlx.DB) *Accumulator {
	return &Accumulator{
		db:            db,
		roomsTable:    NewRoomsTable(db),
		eventsTable:   NewEventTable(db),
		snapshotTable: NewSnapshotsTable(db),
		entityName:    "server",
	}
}

func (a *Accumulator) strippedEventsForSnapshot(txn *sqlx.Tx, snapID int64) (StrippedEvents, error) {
	snapshot, err := a.snapshotTable.Select(txn, snapID)
	if err != nil {
		return nil, err
	}
	// pull stripped events as this may be huge (think Matrix HQ)
	return a.eventsTable.SelectStrippedEventsByNIDs(txn, true, snapshot.Events)
}

// calculateNewSnapshot works out the new snapshot by combining an old snapshot and a new state event. Events get replaced
// if the tuple of event type/state_key match. A new slice is returned (the inputs are not modified) along with the NID
// that got replaced.
func (a *Accumulator) calculateNewSnapshot(old StrippedEvents, new Event) (StrippedEvents, int64) {
	// TODO: implement dendrite's binary tree diff algorithm?
	tupleKey := func(e Event) string {
		// 0x1f = unit separator
		return e.Type + "\x1f" + e.StateKey
	}
	newTuple := tupleKey(new)
	added := false
	var replacedNID int64
	result := make(StrippedEvents, 0, len(old)+1)
	for _, e := range old {
		existingTuple := tupleKey(e)
		if e.NID == new.NID && existingTuple != newTuple {
			// ruh roh. This should be impossible, but it can happen if the v2 response sends the same
			// event in both state and timeline. We need to alert the operator and whine badly as it means
			// we have lost an event by now.
			log.Warn().Str("new_event_id", new.ID).Str("old_event_id", e.ID).Str("room_id", new.RoomID).Str("type", new.Type).Str("state_key", new.StateKey).Msg(
				"Detected different event IDs with the same NID when rolling forward state. This has resulted in data loss in this room (1 event). " +
					"This can happen when the v2 /sync response sends the same event in both state and timeline sections. " +
					"The event in this log line has been dropped!",
			)
			result = make([]Event, len(old))
			copy(result, old)
			return result, 0
		}
		if existingTuple == newTuple {
			// use the new event
			result = append(result, new)
			added = true
			replacedNID = e.NID
			continue
		}
		// use the old event
		result = append(result, e)
	}
	if !added {
		result = append(result, new)
	}
	return result, replacedNID
}

// Initialise starts a new sync accumulator for the given room using the given state as a baseline.
// This will only take effect if this is the first time the v3 server has seen this room, and it wasn't
// possible to get all events up to the create event (e.g Matrix HQ). Returns true if this call actually
// added new events
//
// This function:
// - Stores these events
// - Sets up the current snapshot based on the state list given.
func (a *Accumulator) Initialise(roomID string, state []json.RawMessage) (bool, error) {
	if len(state) == 0 {
		return false, nil
	}
	addedEvents := false
	err := sqlutil.WithTransaction(a.db, func(txn *sqlx.Tx) error {
		// Attempt to short-circuit. This has to be done inside a transaction to make sure
		// we don't race with multiple calls to Initialise with the same room ID.
		snapshotID, err := a.roomsTable.CurrentAfterSnapshotID(txn, roomID)
		if err != nil {
			return fmt.Errorf("error fetching snapshot id for room %s: %s", roomID, err)
		}
		if snapshotID > 0 {
			// we only initialise rooms once
			log.Info().Str("room_id", roomID).Int64("snapshot_id", snapshotID).Msg("Accumulator.Initialise called but current snapshot already exists, bailing early")
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
		if err := ensureFieldsSet(events); err != nil {
			return fmt.Errorf("events malformed: %s", err)
		}
		numNew, err := a.eventsTable.Insert(txn, events, false)
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
		addedEvents = true
		latestNID := int64(0)
		for _, nid := range nids {
			if nid > latestNID {
				latestNID = nid
			}
		}

		// check for an encryption event
		isEncrypted := false
		isTombstoned := false
		for _, ev := range events {
			if ev.Type == "m.room.encryption" && ev.StateKey == "" {
				isEncrypted = true
			}
			if ev.Type == "m.room.tombstone" && ev.StateKey == "" {
				isTombstoned = true
			}
		}

		// these events do not have a state snapshot ID associated with them as we don't know what
		// order the state events came down in, it's only a snapshot. This means only timeline events
		// will have an associated state snapshot ID on the event.

		// Set the snapshot ID as the current state
		return a.roomsTable.UpdateCurrentAfterSnapshotID(txn, roomID, snapshot.SnapshotID, latestNID, isEncrypted, isTombstoned)
	})
	return addedEvents, err
}

// Accumulate internal state from a user's sync response. The timeline order MUST be in the order
// received from the server. Returns the number of new events in the timeline.
//
// This function does several things:
//   - It ensures all events are persisted in the database. This is shared amongst users.
//   - If all events have been stored before, then it short circuits and returns.
//     This is because we must have already processed this part of the timeline in order for the event
//     to exist in the database, and the sync stream is already linearised for us.
//   - Else it creates a new room state snapshot if the timeline contains state events (as this now represents the current state)
//   - It adds entries to the membership log for membership events.
func (a *Accumulator) Accumulate(roomID string, prevBatch string, timeline []json.RawMessage) (numNew int, latestNID int64, err error) {
	if len(timeline) == 0 {
		return 0, 0, nil
	}
	err = sqlutil.WithTransaction(a.db, func(txn *sqlx.Tx) error {
		// Insert the events. Check for duplicates which can happen in the real world when joining
		// Matrix HQ on Synapse.
		events := make([]Event, 0, len(timeline))
		dedupedTimeline := make([]json.RawMessage, 0, len(timeline))
		seenEvents := make(map[string]struct{})
		for i := range timeline {
			e := Event{
				JSON:   timeline[i],
				RoomID: roomID,
			}
			if err := ensureFieldsSetOnEvent(&e); err != nil {
				return fmt.Errorf("event malformed: %s", err)
			}
			if _, ok := seenEvents[e.ID]; ok {
				log.Warn().Str("event_id", e.ID).Str("room_id", roomID).Msg(
					"Accumulator.Accumulate: seen the same event ID twice, ignoring",
				)
				continue
			}
			if i == 0 && prevBatch != "" {
				// tag the first timeline event with the prev batch token
				e.PrevBatch = sql.NullString{
					String: prevBatch,
					Valid:  true,
				}
			}
			events = append(events, e)
			dedupedTimeline = append(dedupedTimeline, timeline[i])
			seenEvents[e.ID] = struct{}{}
		}
		numNew, err = a.eventsTable.Insert(txn, events, false)
		if err != nil {
			return err
		}
		if numNew == 0 {
			// nothing to do, we already know about these events
			return nil
		}

		// The last numNew events are new
		newEvents := dedupedTimeline[len(dedupedTimeline)-numNew:]

		// Decorate the new events with useful information
		var newEventsDec []struct {
			JSON    gjson.Result
			NID     int64
			IsState bool
		}
		var newEventIDs []string
		for _, ev := range newEvents {
			var evDec struct {
				JSON    gjson.Result
				NID     int64
				IsState bool
			}
			eventJSON := gjson.ParseBytes(ev)
			newEventIDs = append(newEventIDs, eventJSON.Get("event_id").Str) // track the event IDs for mapping to NIDs
			evDec.JSON = eventJSON
			if eventJSON.Get("state_key").Exists() {
				evDec.IsState = true
			}
			newEventsDec = append(newEventsDec, evDec)
		}

		newEventNIDs, err := a.eventsTable.SelectNIDsByIDs(txn, newEventIDs)
		if err != nil {
			return err
		}
		if len(newEventNIDs) != len(newEventIDs) {
			log.Error().Strs("asked", newEventIDs).Ints64("gots", newEventNIDs).Msg("missing events in database!")
			return fmt.Errorf("failed to extract nids from inserted events, asked for %d got %d", len(newEventIDs), len(newEventNIDs))
		}
		for i, nid := range newEventNIDs {
			newEventsDec[i].NID = nid
			// assign the highest nid value to the latest nid.
			// we'll return this to the caller so they can stay in-sync
			if nid > latestNID {
				latestNID = nid
			}
		}

		// check if these new events enable encryption / tombstone the room
		isEncrypted := false
		isTombstoned := false

		// Given a timeline of [E1, E2, S3, E4, S5, S6, E7] (E=message event, S=state event)
		// And a prior state snapshot of SNAP0 then the BEFORE snapshot IDs are grouped as:
		// E1,E2,S3 => SNAP0
		// E4, S5 => (SNAP0 + S3)
		// S6 => (SNAP0 + S3 + S5)
		// E7 => (SNAP0 + S3 + S5 + S6)
		// We can track this by loading the current snapshot ID (after snapshot) then rolling forward
		// the timeline until we hit a state event, at which point we make a new snapshot but critically
		// do NOT assign the new state event in the snapshot so as to represent the state before the event.
		snapID, err := a.roomsTable.CurrentAfterSnapshotID(txn, roomID)
		if err != nil {
			return err
		}
		for _, ev := range newEventsDec {
			var replacesNID int64
			// the snapshot ID we assign to this event is unaffected by whether /this/ event is state or not,
			// as this is the before snapshot ID.
			beforeSnapID := snapID

			if ev.IsState {
				// make a new snapshot and update the snapshot ID
				var oldStripped StrippedEvents
				if snapID != 0 {
					oldStripped, err = a.strippedEventsForSnapshot(txn, snapID)
					if err != nil {
						return fmt.Errorf("failed to load stripped state events for snapshot %d: %s", snapID, err)
					}
				}
				newStateEvent := Event{
					NID:      ev.NID,
					Type:     ev.JSON.Get("type").Str,
					StateKey: ev.JSON.Get("state_key").Str,
					ID:       ev.JSON.Get("event_id").Str,
					RoomID:   roomID,
				}
				newStripped, replacedNID := a.calculateNewSnapshot(oldStripped, newStateEvent)
				if err != nil {
					return err
				}
				replacesNID = replacedNID
				newSnapshot := &SnapshotRow{
					RoomID: roomID,
					Events: newStripped.NIDs(),
				}
				if err = a.snapshotTable.Insert(txn, newSnapshot); err != nil {
					return fmt.Errorf("failed to insert new snapshot: %w", err)
				}
				snapID = newSnapshot.SnapshotID
				if newStateEvent.Type == "m.room.encryption" && newStateEvent.StateKey == "" {
					isEncrypted = true
				}
				if newStateEvent.Type == "m.room.tombstone" && newStateEvent.StateKey == "" {
					isTombstoned = true
				}
			}
			if err := a.eventsTable.UpdateBeforeSnapshotID(txn, ev.NID, beforeSnapID, replacesNID); err != nil {
				return err
			}
		}

		// the last fetched snapshot ID is the current one
		if err = a.roomsTable.UpdateCurrentAfterSnapshotID(txn, roomID, snapID, latestNID, isEncrypted, isTombstoned); err != nil {
			return fmt.Errorf("failed to UpdateCurrentSnapshotID to %d: %w", snapID, err)
		}
		return nil
	})
	return numNew, latestNID, err
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
