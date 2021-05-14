package syncv3

import (
	"database/sql"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
)

type Session struct {
	ID              string `db:"session_id"`
	DeviceID        string `db:"device_id"`
	LastToDeviceACK string `db:"last_to_device_ack"`
}

type Sessions struct {
	db                *sqlx.DB
	upsertSessionStmt *sql.Stmt
}

func NewSessions(postgresURI string) *Sessions {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	// make sure tables are made
	db.MustExec(`
	CREATE SEQUENCE IF NOT EXISTS syncv3_session_id_seq;
	CREATE TABLE IF NOT EXISTS syncv3_sessions (
		session_id BIGINT PRIMARY KEY DEFAULT nextval('syncv3_session_id_seq'),
		-- user_id TEXT NOT NULL, (we don't know the user ID from the access token alone, at least in dendrite!)
		device_id TEXT NOT NULL,
		last_to_device_ack TEXT NOT NULL,
		CONSTRAINT syncv3_sessions_unique UNIQUE (device_id, session_id)
	);
	`)
	return &Sessions{
		db: db,
	}
}

func (s *Sessions) NewSession(deviceID string) (*Session, error) {
	var id string
	err := s.db.QueryRow("INSERT INTO syncv3_sessions(device_id, last_to_device_ack) VALUES($1,'') RETURNING session_id", deviceID).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &Session{
		ID:       id,
		DeviceID: deviceID,
	}, nil
}

func (s *Sessions) Session(sessionID, deviceID string) (*Session, error) {
	var result Session
	err := s.db.Get(&result, "SELECT * FROM syncv3_sessions WHERE session_id=$1 AND device_id=$2", sessionID, deviceID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &result, nil
}
