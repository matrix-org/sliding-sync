package state

import (
	"database/sql"
	"fmt"
	"math"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sqlutil"
)

const (
	EventsStart = -1
	EventsEnd   = math.MaxInt64 - 1
)

type Event struct {
	NID        int64  `db:"event_nid"`
	Type       string `db:"event_type"`
	StateKey   string `db:"state_key"`
	Membership string `db:"membership"`
	// whether this was part of a v2 state response and hence not part of the timeline
	IsState bool `db:"is_state"`
	// This is a snapshot ID which corresponds to some room state BEFORE this event has been applied.
	BeforeStateSnapshotID int64  `db:"before_state_snapshot_id"`
	ReplacesNID           int64  `db:"event_replaces_nid"`
	ID                    string `db:"event_id"`
	RoomID                string `db:"room_id"`
	// not all events include a prev batch (e.g if it was part of state not timeline, and only the first
	// event in a timeline has a prev_batch attached), but we'll look for the 'closest' prev batch
	// when returning these tokens to the caller (closest = next newest, assume clients de-dupe)
	PrevBatch sql.NullString `db:"prev_batch"`
	// stripped events will be missing this field
	JSON []byte `db:"event"`
}

func (ev *Event) ensureFieldsSetOnEvent() error {
	evJSON := gjson.ParseBytes(ev.JSON)
	if ev.RoomID == "" {
		roomIDResult := evJSON.Get("room_id")
		if !roomIDResult.Exists() || roomIDResult.Str == "" {
			return fmt.Errorf("event missing room_id key")
		}
		ev.RoomID = roomIDResult.Str
	}
	if ev.ID == "" {
		eventIDResult := evJSON.Get("event_id")
		if !eventIDResult.Exists() || eventIDResult.Str == "" {
			return fmt.Errorf("event JSON missing event_id key")
		}
		ev.ID = eventIDResult.Str
	}
	if ev.Type == "" {
		typeResult := evJSON.Get("type")
		if !typeResult.Exists() || typeResult.Str == "" {
			return fmt.Errorf("event JSON missing type key")
		}
		ev.Type = typeResult.Str
	}
	if ev.StateKey == "" {
		// valid for this to be "" on message events
		ev.StateKey = evJSON.Get("state_key").Str
	}
	if ev.StateKey != "" && ev.Type == "m.room.member" {
		membershipResult := evJSON.Get("content.membership")
		if !membershipResult.Exists() || membershipResult.Str == "" {
			return fmt.Errorf("membership event missing membership key")
		}
		// genuine changes mark the membership event
		if internal.IsMembershipChange(evJSON) {
			ev.Membership = membershipResult.Str
		} else {
			// profile changes have _ prefix.
			ev.Membership = "_" + membershipResult.Str
		}
	}
	return nil
}

type StrippedEvents []Event

func (se StrippedEvents) NIDs() (membershipNIDs, otherNIDs []int64) {
	for _, s := range se {
		if s.Type == "m.room.member" {
			membershipNIDs = append(membershipNIDs, s.NID)
		} else {
			otherNIDs = append(otherNIDs, s.NID)
		}
	}
	return
}

type membershipEvent struct {
	Event
	StateKey   string
	Membership string
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
		event_type TEXT NOT NULL,
		state_key TEXT NOT NULL,
		prev_batch TEXT,
		membership TEXT,
		is_state BOOLEAN NOT NULL, -- is this event part of the v2 state response?
		event BYTEA NOT NULL
	);
	-- index for querying all joined rooms for a given user
	CREATE INDEX IF NOT EXISTS syncv3_events_type_sk_idx ON syncv3_events(event_type, state_key);
	-- index for querying membership deltas in particular rooms
	CREATE INDEX IF NOT EXISTS syncv3_events_type_room_nid_idx ON syncv3_events(event_type, room_id, event_nid);
	-- index for querying events in a given room
	CREATE INDEX IF NOT EXISTS syncv3_nid_room_state_idx ON syncv3_events(room_id, event_nid, is_state);

	CREATE UNIQUE INDEX IF NOT EXISTS syncv3_events_room_event_nid_type_skey_idx ON syncv3_events(event_nid, event_type, state_key);
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

// Insert events into the event table. Returns a map of event ID to NID for new events only.
func (t *EventTable) Insert(txn *sqlx.Tx, events []Event, checkFields bool) (map[string]int, error) {
	if checkFields {
		ensureFieldsSet(events)
	}
	result := make(map[string]int)
	for i := range events {
		if !gjson.GetBytes(events[i].JSON, "unsigned.txn_id").Exists() {
			continue
		}
		js, err := sjson.DeleteBytes(events[i].JSON, "unsigned.txn_id")
		if err != nil {
			return nil, err
		}
		events[i].JSON = js
	}
	chunks := sqlutil.Chunkify(8, MaxPostgresParameters, EventChunker(events))
	var eventID string
	var eventNID int
	for _, chunk := range chunks {
		rows, err := txn.NamedQuery(`
		INSERT INTO syncv3_events (event_id, event, event_type, state_key, room_id, membership, prev_batch, is_state)
        VALUES (:event_id, :event, :event_type, :state_key, :room_id, :membership, :prev_batch, :is_state) ON CONFLICT (event_id) DO NOTHING RETURNING event_id, event_nid`, chunk)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			if err := rows.Scan(&eventID, &eventNID); err != nil {
				return nil, err
			}
			result[eventID] = eventNID
		}
	}
	return result, nil
}

// select events in a list of nids or ids, depending on the query. Provides flexibility to query on NID or ID, as well as
// the ability to pull stripped events or normal events
func (t *EventTable) selectAny(txn *sqlx.Tx, numWanted int, queryStr string, pqArray interface{}) (events []Event, err error) {
	if txn != nil {
		err = txn.Select(&events, queryStr, pqArray)
	} else {
		err = t.db.Select(&events, queryStr, pqArray)
	}
	if numWanted > 0 {
		if numWanted != len(events) {
			return nil, fmt.Errorf("events table query %s got %d events wanted %d. err=%s", queryStr, len(events), numWanted, err)
		}
	}
	return
}

func (t *EventTable) SelectByNIDs(txn *sqlx.Tx, verifyAll bool, nids []int64) (events []Event, err error) {
	wanted := 0
	if verifyAll {
		wanted = len(nids)
	}
	return t.selectAny(txn, wanted, `
	SELECT event_nid, event_id, event, event_type, state_key, room_id, before_state_snapshot_id, membership, event_replaces_nid FROM syncv3_events
	WHERE event_nid = ANY ($1) ORDER BY event_nid ASC;`, pq.Int64Array(nids))
}

func (t *EventTable) SelectByIDs(txn *sqlx.Tx, verifyAll bool, ids []string) (events []Event, err error) {
	wanted := 0
	if verifyAll {
		wanted = len(ids)
	}
	return t.selectAny(txn, wanted, `
	SELECT event_nid, event_id, event, event_type, state_key, room_id, before_state_snapshot_id, membership FROM syncv3_events
	WHERE event_id = ANY ($1) ORDER BY event_nid ASC;`, pq.StringArray(ids))
}

func (t *EventTable) SelectNIDsByIDs(txn *sqlx.Tx, ids []string) (nids map[string]int64, err error) {
	// Select NIDs using a single parameter which is a string array
	// https://stackoverflow.com/questions/52712022/what-is-the-most-performant-way-to-rewrite-a-large-in-clause
	result := make(map[string]int64, len(ids))
	rows := []struct {
		NID int64  `db:"event_nid"`
		ID  string `db:"event_id"`
	}{}
	err = txn.Select(&rows, "SELECT event_nid, event_id FROM syncv3_events WHERE event_id = ANY ($1);", pq.StringArray(ids))
	for _, row := range rows {
		result[row.ID] = row.NID
	}
	return result, err
}

func (t *EventTable) SelectStrippedEventsByNIDs(txn *sqlx.Tx, verifyAll bool, nids []int64) (StrippedEvents, error) {
	wanted := 0
	if verifyAll {
		wanted = len(nids)
	}
	// don't include the 'event' column
	return t.selectAny(txn, wanted, `
	SELECT event_nid, event_id, event_type, state_key, room_id, before_state_snapshot_id FROM syncv3_events
	WHERE event_nid = ANY ($1) ORDER BY event_nid ASC;`, pq.Int64Array(nids))
}

func (t *EventTable) SelectStrippedEventsByIDs(txn *sqlx.Tx, verifyAll bool, ids []string) (StrippedEvents, error) {
	wanted := 0
	if verifyAll {
		wanted = len(ids)
	}
	// don't include the 'event' column
	return t.selectAny(txn, wanted, `
	SELECT event_nid, event_id, event_type, state_key, room_id, before_state_snapshot_id FROM syncv3_events
	WHERE event_id = ANY ($1) ORDER BY event_nid ASC;`, pq.StringArray(ids))

}

// UpdateBeforeSnapshotID sets the before_state_snapshot_id field to `snapID` for the given NIDs.
func (t *EventTable) UpdateBeforeSnapshotID(txn *sqlx.Tx, eventNID, snapID, replacesNID int64) error {
	_, err := txn.Exec(
		`UPDATE syncv3_events SET before_state_snapshot_id=$1, event_replaces_nid=$2 WHERE event_nid = $3`, snapID, replacesNID, eventNID,
	)
	return err
}

// query the latest events in each of the room IDs given, using highestNID as the highest event.
func (t *EventTable) LatestEventInRooms(txn *sqlx.Tx, roomIDs []string, highestNID int64) (events []Event, err error) {
	// the position (event nid) may be for a random different room, so we need to find the highest nid <= this position for this room
	err = txn.Select(
		&events,
		`SELECT event_nid, room_id, event_replaces_nid, before_state_snapshot_id, event_type, state_key, event FROM syncv3_events
		WHERE event_nid IN (SELECT max(event_nid) FROM syncv3_events WHERE event_nid <= $1 AND room_id = ANY($2) GROUP BY room_id)`,
		highestNID, pq.StringArray(roomIDs),
	)
	if err == sql.ErrNoRows {
		err = nil
	}
	return
}

func (t *EventTable) SelectEventsBetween(txn *sqlx.Tx, roomID string, lowerExclusive, upperInclusive int64, limit int) ([]Event, error) {
	var events []Event
	err := txn.Select(&events, `SELECT event_nid, event FROM syncv3_events WHERE event_nid > $1 AND event_nid <= $2 AND room_id = $3 ORDER BY event_nid ASC LIMIT $4`,
		lowerExclusive, upperInclusive, roomID, limit,
	)
	return events, err
}

func (t *EventTable) SelectLatestEventsBetween(txn *sqlx.Tx, roomID string, lowerExclusive, upperInclusive int64, limit int) ([]Event, error) {
	var events []Event
	// do not pull in events which were in the v2 state block
	err := txn.Select(&events, `SELECT event_nid, event FROM syncv3_events WHERE event_nid > $1 AND event_nid <= $2 AND room_id = $3 AND is_state=FALSE ORDER BY event_nid DESC LIMIT $4`,
		lowerExclusive, upperInclusive, roomID, limit,
	)
	return events, err
}

func (t *EventTable) selectLatestEventInAllRooms(txn *sqlx.Tx) ([]Event, error) {
	result := []Event{}
	rows, err := txn.Query(
		`SELECT room_id, event FROM syncv3_events WHERE event_nid in (SELECT MAX(event_nid) FROM syncv3_events GROUP BY room_id)`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.RoomID, &ev.JSON); err != nil {
			return nil, err
		}
		result = append(result, ev)
	}
	return result, nil
}

// Select all events between the bounds matching the type, state_key given.
// Used to work out which rooms the user was joined to at a given point in time.
func (t *EventTable) SelectEventsWithTypeStateKey(eventType, stateKey string, lowerExclusive, upperInclusive int64) ([]Event, error) {
	var events []Event
	err := t.db.Select(&events,
		`SELECT event_nid, room_id, event FROM syncv3_events
		WHERE event_nid > $1 AND event_nid <= $2 AND event_type = $3 AND state_key = $4
		ORDER BY event_nid ASC`,
		lowerExclusive, upperInclusive, eventType, stateKey,
	)
	return events, err
}

// Select all events between the bounds matching the type, state_key given, in the rooms specified only.
// Used to work out which rooms the user was joined to at a given point in time.
func (t *EventTable) SelectEventsWithTypeStateKeyInRooms(roomIDs []string, eventType, stateKey string, lowerExclusive, upperInclusive int64) ([]Event, error) {
	var events []Event
	query, args, err := sqlx.In(
		`SELECT event_nid, room_id, event FROM syncv3_events
		WHERE event_nid > ? AND event_nid <= ? AND event_type = ? AND state_key = ? AND room_id IN (?)
		ORDER BY event_nid ASC`, lowerExclusive, upperInclusive, eventType, stateKey, roomIDs,
	)
	if err != nil {
		return nil, err
	}

	err = t.db.Select(&events,
		t.db.Rebind(query), args...,
	)
	return events, err
}

// Select all events matching the given event type in a room. Used to implement the room member stream (paginated room lists)
func (t *EventTable) SelectEventNIDsWithTypeInRoom(txn *sqlx.Tx, eventType string, limit int, targetRoom string, lowerExclusive, upperInclusive int64) (eventNIDs []int64, err error) {
	err = txn.Select(
		&eventNIDs, `SELECT event_nid FROM syncv3_events WHERE event_nid > $1 AND event_nid <= $2 AND event_type = $3 AND room_id = $4 ORDER BY event_nid ASC LIMIT $5`,
		lowerExclusive, upperInclusive, eventType, targetRoom, limit,
	)
	return
}

// SelectClosestPrevBatchByID is the same as SelectClosestPrevBatch but works on event IDs not NIDs
func (t *EventTable) SelectClosestPrevBatchByID(roomID string, eventID string) (prevBatch string, err error) {
	err = t.db.QueryRow(
		`SELECT prev_batch FROM syncv3_events WHERE prev_batch IS NOT NULL AND room_id=$1 AND event_nid >= (
			SELECT event_nid FROM syncv3_events WHERE event_id = $2
		) LIMIT 1`, roomID, eventID,
	).Scan(&prevBatch)
	if err == sql.ErrNoRows {
		err = nil
	}
	return
}

// Select the closest prev batch token for the provided event NID. Returns the empty string if there
// is no closest.
func (t *EventTable) SelectClosestPrevBatch(roomID string, eventNID int64) (prevBatch string, err error) {
	err = t.db.QueryRow(
		`SELECT prev_batch FROM syncv3_events WHERE prev_batch IS NOT NULL AND room_id=$1 AND event_nid >= $2 LIMIT 1`, roomID, eventNID,
	).Scan(&prevBatch)
	if err == sql.ErrNoRows {
		err = nil
	}
	return
}

type EventChunker []Event

func (c EventChunker) Len() int {
	return len(c)
}
func (c EventChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}

func ensureFieldsSet(events []Event) error {
	// ensure fields are set
	for i := range events {
		ev := events[i]
		if err := ev.ensureFieldsSetOnEvent(); err != nil {
			return err
		}
		events[i] = ev
	}
	return nil
}
