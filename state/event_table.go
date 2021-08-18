package state

import (
	"database/sql"
	"fmt"
	"math"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/tidwall/gjson"
)

const (
	EventsStart = -1
	EventsEnd   = math.MaxInt64 - 1
)

type Event struct {
	NID int64 `db:"event_nid"`
	// This is a snapshot ID which corresponds to some room state BEFORE this event has been applied.
	BeforeStateSnapshotID int    `db:"before_state_snapshot_id"`
	ID                    string `db:"event_id"`
	RoomID                string `db:"room_id"`
	JSON                  []byte `db:"event"`
}

type StrippedEvent struct {
	NID      int64  `db:"event_nid"`
	Type     string `db:"type"`
	StateKey string `db:"state_key"`
}

type StrippedEvents []StrippedEvent

func (se StrippedEvents) NIDs() (result []int64) {
	for _, s := range se {
		result = append(result, s.NID)
	}
	return
}

// EventTable stores events. A unique numeric ID is associated with each event.
type EventTable struct {
	db *sqlx.DB
}

// NewEventTable makes a new EventTable
func NewEventTable(db *sqlx.DB) *EventTable {
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_event_nids_seq;
	CREATE TABLE IF NOT EXISTS syncv3_events (
		event_nid BIGINT PRIMARY KEY NOT NULL DEFAULT nextval('syncv3_event_nids_seq'),
		event_id TEXT NOT NULL UNIQUE,
		before_state_snapshot_id BIGINT NOT NULL DEFAULT 0,
		-- which nid gets replaced in the snapshot with event_nid
		event_replaces_nid BIGINT NOT NULL DEFAULT 0,
		room_id TEXT NOT NULL,
		event JSONB NOT NULL
	);
	`)
	return &EventTable{db}
}

func (t *EventTable) SelectHighestNID() (highest int64, err error) {
	var result sql.NullInt64
	err = t.db.QueryRow(
		`SELECT MAX(event_nid) as e FROM syncv3_events`,
	).Scan(&result)
	if result.Valid {
		highest = result.Int64
	}
	return
}

// Insert events into the event table. Returns the number of rows added. If the number of rows is >0,
// and the list of events is in sync stream order, it can be inferred that the last element(s) are new.
func (t *EventTable) Insert(txn *sqlx.Tx, events []Event) (int, error) {
	// ensure event_id is set
	for i := range events {
		ev := events[i]
		if ev.RoomID == "" {
			roomIDResult := gjson.GetBytes(ev.JSON, "room_id")
			if !roomIDResult.Exists() || roomIDResult.Str == "" {
				return 0, fmt.Errorf("event missing room_id key")
			}
			ev.RoomID = roomIDResult.Str
		}
		if ev.ID == "" {
			eventIDResult := gjson.GetBytes(ev.JSON, "event_id")
			if !eventIDResult.Exists() || eventIDResult.Str == "" {
				return 0, fmt.Errorf("event JSON missing event_id key")
			}
			ev.ID = eventIDResult.Str
		}
		events[i] = ev
	}
	chunks := sqlutil.Chunkify(4, 65535, EventChunker(events))
	var rowsAffected int64
	for _, chunk := range chunks {
		result, err := txn.NamedExec(`INSERT INTO syncv3_events (event_id, room_id, event)
        VALUES (:event_id, :room_id, :event) ON CONFLICT (event_id) DO NOTHING`, chunk)
		if err != nil {
			return 0, err
		}
		ra, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		rowsAffected += ra
	}
	return int(rowsAffected), nil
}

// SelectByNIDs allows txn to be nil
func (t *EventTable) SelectByNIDs(txn *sqlx.Tx, nids []int64) (events []Event, err error) {
	query, args, err := sqlx.In("SELECT event_nid, event_id, event FROM syncv3_events WHERE event_nid IN (?) ORDER BY event_nid ASC;", nids)
	if txn != nil {
		query = txn.Rebind(query)
		if err != nil {
			return nil, err
		}
		err = txn.Select(&events, query, args...)
	} else {
		query = t.db.Rebind(query)
		if err != nil {
			return nil, err
		}
		err = t.db.Select(&events, query, args...)
	}
	return
}

func (t *EventTable) SelectByIDs(txn *sqlx.Tx, ids []string) (events []Event, err error) {
	query, args, err := sqlx.In("SELECT event_nid, event_id, event FROM syncv3_events WHERE event_id IN (?) ORDER BY event_nid ASC;", ids)
	query = txn.Rebind(query)
	if err != nil {
		return nil, err
	}
	err = txn.Select(&events, query, args...)
	return
}

func (t *EventTable) SelectNIDsByIDs(txn *sqlx.Tx, ids []string) (nids []int64, err error) {
	query, args, err := sqlx.In("SELECT event_nid FROM syncv3_events WHERE event_id IN (?) ORDER BY event_nid ASC;", ids)
	query = txn.Rebind(query)
	if err != nil {
		return nil, err
	}
	err = txn.Select(&nids, query, args...)
	return
}

func (t *EventTable) SelectStrippedEventsByNIDs(txn *sqlx.Tx, nids []int64) (StrippedEvents, error) {
	query, args, err := sqlx.In("SELECT event_nid, event->>'type' as type, event->>'state_key' as state_key FROM syncv3_events WHERE event_nid IN (?) ORDER BY event_nid ASC;", nids)
	query = txn.Rebind(query)
	if err != nil {
		return nil, err
	}
	var events []StrippedEvent
	err = txn.Select(&events, query, args...)
	return events, err
}

func (t *EventTable) SelectStrippedEventsByIDs(txn *sqlx.Tx, ids []string) (StrippedEvents, error) {
	query, args, err := sqlx.In("SELECT event_nid, event->>'type' as type, event->>'state_key' as state_key FROM syncv3_events WHERE event_id IN (?) ORDER BY event_nid ASC;", ids)
	query = txn.Rebind(query)
	if err != nil {
		return nil, err
	}
	var events []StrippedEvent
	err = txn.Select(&events, query, args...)
	// we often call this function to pull out nids for events we just inserted, so it's important that
	// we grab all the events else we might miss putting some events into a snapshot
	if len(ids) != len(events) {
		diff := len(ids) - len(events)
		return nil, fmt.Errorf("%d events are missing in the database from this list: %v", diff, ids)
	}
	return events, err
}

// UpdateBeforeSnapshotID sets the before_state_snapshot_id field to `snapID` for the given NIDs.
func (t *EventTable) UpdateBeforeSnapshotID(txn *sqlx.Tx, eventNID, snapID, replacesNID int64) error {
	_, err := txn.Exec(
		`UPDATE syncv3_events SET before_state_snapshot_id=$1, event_replaces_nid=$2 WHERE event_nid = $3`, snapID, replacesNID, eventNID,
	)
	return err
}

func (t *EventTable) BeforeStateSnapshotIDForEventNID(txn *sqlx.Tx, roomID string, eventNID int64) (lastEventNID, replacesNID, snapID int64, err error) {
	// the position (event nid) may be for a random different room, so we need to find the highest nid <= this position for this room
	err = txn.QueryRow(
		`SELECT event_nid, event_replaces_nid, before_state_snapshot_id FROM syncv3_events WHERE event_nid =
		(SELECT MAX(event_nid) FROM syncv3_events WHERE room_id = $1 AND event_nid <= $2)`, roomID, eventNID,
	).Scan(&lastEventNID, &replacesNID, &snapID)
	return
}

func (t *EventTable) SelectEventsBetween(txn *sqlx.Tx, roomID string, lowerExclusive, upperInclusive int64, limit int) ([]Event, error) {
	var events []Event
	err := txn.Select(&events, `SELECT event_nid, event FROM syncv3_events WHERE event_nid > $1 AND event_nid <= $2 AND room_id = $3 ORDER BY event_nid ASC LIMIT $4`,
		lowerExclusive, upperInclusive, roomID, limit,
	)
	return events, err
}

type EventChunker []Event

func (c EventChunker) Len() int {
	return len(c)
}
func (c EventChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}
