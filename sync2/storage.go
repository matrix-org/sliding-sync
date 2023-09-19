package sync2

import (
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type Storage struct {
	DevicesTable *DevicesTable
	TokensTable  *TokensTable
	DB           *sqlx.DB
}

func NewStore(postgresURI, secret string) *Storage {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		sentry.CaptureException(err)
		// TODO: if we panic(), will sentry have a chance to flush the event?
		logger.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	return NewStoreWithDB(db, secret)
}

func NewStoreWithDB(db *sqlx.DB, secret string) *Storage {
	return &Storage{
		DevicesTable: NewDevicesTable(db),
		TokensTable:  NewTokensTable(db, secret),
		DB:           db,
	}
}

func (s *Storage) Teardown() {
	err := s.DB.Close()
	if err != nil {
		panic("V2Storage.Teardown: " + err.Error())
	}
}

// StartDeleteExpiredTokenTicker starts a go routine which
// periodically (1h) checks for expired tokens to delete.
func (s *Storage) StartDeleteExpiredTokenTicker(duration time.Duration) {
	ticker := time.NewTicker(duration)
	go func() {
		for range ticker.C {
			deleted, err := s.TokensTable.deleteExpiredTokensAfter(30 * 24 * time.Hour)
			if err != nil {
				logger.Warn().Err(err).Msg("failed to delete expired tokens")
			} else {
				logger.Trace().Int64("deleted", deleted).Msg("deleted expired tokens")
			}
		}
	}()
}
