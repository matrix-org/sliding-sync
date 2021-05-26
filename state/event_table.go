package state

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/tidwall/gjson"
)

type Event struct {
	NID  int    `db:"event_nid"`
	ID   string `db:"event_id"`
	JSON []byte `db:"event"`
}

type EventTable struct {
	db *sqlx.DB
}

func NewEventTable(postgresURI string) *EventTable {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_event_nids_seq;
	CREATE TABLE IF NOT EXISTS syncv3_events (
		event_nid BIGINT PRIMARY KEY NOT NULL DEFAULT nextval('syncv3_event_nids_seq'),
		event_id TEXT NOT NULL UNIQUE,
		event JSONB NOT NULL
	);
	`)
	return &EventTable{
		db: db,
	}
}

func (t *EventTable) Insert(events []Event) error {
	// ensure event_id is set
	for i := range events {
		ev := events[i]
		if ev.ID != "" {
			continue
		}
		eventIDResult := gjson.GetBytes(ev.JSON, "event_id")
		if !eventIDResult.Exists() || eventIDResult.Str == "" {
			return fmt.Errorf("event JSON missing event_id key")
		}
		ev.ID = eventIDResult.Str
		events[i] = ev
	}
	_, err := t.db.NamedExec(`INSERT INTO syncv3_events (event_id, event)
        VALUES (:event_id, :event) ON CONFLICT (event_id) DO NOTHING`, events)
	return err
}

func (t *EventTable) SelectByNIDs(nids []int) (events []Event, err error) {
	query, args, err := sqlx.In("SELECT * FROM syncv3_events WHERE event_nid IN (?);", nids)
	query = t.db.Rebind(query)
	if err != nil {
		return nil, err
	}
	err = t.db.Select(&events, query, args...)
	return
}

func (t *EventTable) SelectByIDs(ids []string) (events []Event, err error) {
	query, args, err := sqlx.In("SELECT * FROM syncv3_events WHERE event_id IN (?);", ids)
	query = t.db.Rebind(query)
	if err != nil {
		return nil, err
	}
	err = t.db.Select(&events, query, args...)
	return
}
