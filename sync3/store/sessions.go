package store

import (
	"database/sql"
	"encoding/json"
	"os"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/streams"
	"github.com/rs/zerolog"
)

var log = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type Sessions struct {
	db      *sqlx.DB
	v2store *sync2.Storage
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
		device_id TEXT NOT NULL,
		last_confirmed_token TEXT NOT NULL,
		last_unconfirmed_token TEXT NOT NULL,
		CONSTRAINT syncv3_sessions_unique UNIQUE (device_id, session_id)
	);
	CREATE SEQUENCE IF NOT EXISTS syncv3_filter_id_seq;
	CREATE TABLE IF NOT EXISTS syncv3_filters (
		filter_id BIGINT PRIMARY KEY DEFAULT nextval('syncv3_filter_id_seq'),
		session_id BIGINT NOT NULL,
		req_json TEXT NOT NULL
	);
	`)

	return &Sessions{
		db:      db,
		v2store: sync2.NewStore(postgresURI),
	}
}

func (s *Sessions) NewSession(deviceID string) (*sync3.Session, error) {
	var session *sync3.Session
	err := sqlutil.WithTransaction(s.db, func(txn *sqlx.Tx) error {
		// make a new session
		var id int64
		err := txn.QueryRow(`
			INSERT INTO syncv3_sessions(device_id, last_confirmed_token, last_unconfirmed_token)
			VALUES($1, '', '') RETURNING session_id`,
			deviceID,
		).Scan(&id)
		if err != nil {
			return err
		}
		// insert v2 bits - this isn't done in a txn but that's fine, as we are just inserting a static ID
		// at this point
		dev, err := s.v2store.InsertDevice(deviceID)
		if err != nil {
			return err
		}
		session = &sync3.Session{
			ID: id,
			V2: dev,
		}
		return nil
	})
	return session, err
}

func (s *Sessions) Session(sessionID int64, deviceID string) (*sync3.Session, error) {
	var result sync3.Session
	// Important not just to use sessionID as that can be set by anyone as a query param
	// Only the device ID is secure (it's a hash of the bearer token)
	err := s.db.Get(&result,
		`SELECT last_confirmed_token, last_unconfirmed_token FROM syncv3_sessions
		WHERE session_id=$1 AND device_id=$2`,
		sessionID, deviceID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dev, err := s.v2store.Device(deviceID)
	if err != nil {
		return nil, err
	}
	result.ID = sessionID
	result.V2 = dev
	return &result, nil
}

func (s *Sessions) ConfirmedSessionTokens(deviceID string) (tokens []string, err error) {
	err = s.db.Select(&tokens, `SELECT last_confirmed_token FROM syncv3_sessions WHERE device_id=$1`, deviceID)
	return
}

// UpdateLastTokens updates the latest tokens for this session
func (s *Sessions) UpdateLastTokens(sessionID int64, confirmed, unconfirmed string) error {
	_, err := s.db.Exec(
		`UPDATE syncv3_sessions SET last_confirmed_token = $1, last_unconfirmed_token = $2 WHERE session_id = $3`,
		confirmed, unconfirmed, sessionID,
	)
	return err
}

func (s *Sessions) UpdateDeviceSince(deviceID, since string) error {
	return s.v2store.UpdateDeviceSince(deviceID, since)
}

func (s *Sessions) UpdateUserIDForDevice(deviceID, userID string) error {
	return s.v2store.UpdateUserIDForDevice(deviceID, userID)
}

// Insert a new filter for this session. The returned filter ID should be inserted into the since token
// so the request filter can be extracted again.
func (s *Sessions) InsertFilter(sessionID int64, req *streams.Request) (filterID int64, err error) {
	j, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	err = s.db.QueryRow(`INSERT INTO syncv3_filters(session_id, req_json) VALUES($1,$2) RETURNING filter_id`, sessionID, string(j)).Scan(&filterID)
	return
}

// Filter returns the filter for the session ID and filter ID given. If a filter ID is given which is unknown, an
// error is returned as filters should always be known to the server.
func (s *Sessions) Filter(sessionID int64, filterID int64) (*streams.Request, error) {
	// we need the session ID to make sure users can't use other user's filters
	var j string
	err := s.db.QueryRow(`SELECT req_json FROM syncv3_filters WHERE session_id=$1 AND filter_id=$2`, sessionID, filterID).Scan(&j)
	if err != nil {
		return nil, err // ErrNoRows is expected and is an error
	}
	var req streams.Request
	if err := json.Unmarshal([]byte(j), &req); err != nil {
		return nil, err
	}
	return &req, nil
}
