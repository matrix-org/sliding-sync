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
	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(80)
	db.SetConnMaxLifetime(time.Hour)
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
