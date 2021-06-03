package state

import (
	"fmt"
	"math"

	"github.com/jmoiron/sqlx"
	"github.com/tidwall/gjson"
)

const (
	EventsStart = -1
	EventsEnd   = math.MaxInt64 - 1
)

type Event struct {
	NID    int    `db:"event_nid"`
	ID     string `db:"event_id"`
	RoomID string `db:"room_id"`
	JSON   []byte `db:"event"`
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
}

// NewEventTable makes a new EventTable
func NewEventTable(db *sqlx.DB) *EventTable {
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_event_nids_seq;
	CREATE TABLE IF NOT EXISTS syncv3_events (
		event_nid BIGINT PRIMARY KEY NOT NULL DEFAULT nextval('syncv3_event_nids_seq'),
		event_id TEXT NOT NULL UNIQUE,
		room_id TEXT NOT NULL,
		event JSONB NOT NULL
	);
	`)
	return &EventTable{}
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
	result, err := txn.NamedExec(`INSERT INTO syncv3_events (event_id, room_id, event)
        VALUES (:event_id, :room_id, :event) ON CONFLICT (event_id) DO NOTHING`, events)
	if err != nil {
		return 0, err
	}
	ra, err := result.RowsAffected()
	return int(ra), err
}

func (t *EventTable) SelectByNIDs(txn *sqlx.Tx, nids []int64) (events []Event, err error) {
	query, args, err := sqlx.In("SELECT event_nid, event_id, event FROM syncv3_events WHERE event_nid IN (?) ORDER BY event_nid ASC;", nids)
	query = txn.Rebind(query)
	if err != nil {
		return nil, err
	}
	err = txn.Select(&events, query, args...)
	return
}

func (t *EventTable) SelectByIDs(txn *sqlx.Tx, ids []string) (events []Event, err error) {
	query, args, err := sqlx.In("SELECT event_nid, event_id, event FROM syncv3_events WHERE event_id IN (?);", ids)
	query = txn.Rebind(query)
	if err != nil {
		return nil, err
	}
	err = txn.Select(&events, query, args...)
	return
}

func (t *EventTable) SelectNIDsByIDs(txn *sqlx.Tx, ids []string) (nids []int64, err error) {
	query, args, err := sqlx.In("SELECT event_nid FROM syncv3_events WHERE event_id IN (?);", ids)
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
	return events, err
}

func (t *EventTable) SelectEventsBetween(txn *sqlx.Tx, roomID string, lowerExclusive, upperInclusive int64, limit int) ([]Event, error) {
	var events []Event
	err := txn.Select(&events, `SELECT event_nid, event FROM syncv3_events WHERE event_nid > $1 AND event_nid <= $2 AND room_id = $3 LIMIT $4`,
		lowerExclusive, upperInclusive, roomID, limit,
	)
	return events, err
}
