package sync2

import (
	"github.com/getsentry/sentry-go"
	"github.com/jmoiron/sqlx"
	"github.com/rs/zerolog/log"
)

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
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
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
