package state

import (
	"encoding/json"

	"github.com/jmoiron/sqlx"
)

type Storage struct {
	accumulator   *Accumulator
	TypingTable   *TypingTable
	ToDeviceTable *ToDeviceTable
}

func NewStorage(postgresURI string) *Storage {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	acc := &Accumulator{
		db:                    db,
		roomsTable:            NewRoomsTable(db),
		eventsTable:           NewEventTable(db),
		snapshotTable:         NewSnapshotsTable(db),
		snapshotRefCountTable: NewSnapshotRefCountsTable(db),
		membershipLogTable:    NewMembershipLogTable(db),
		entityName:            "server",
	}
	return &Storage{
		accumulator:   acc,
		TypingTable:   NewTypingTable(db),
		ToDeviceTable: NewToDeviceTable(db),
	}
}

func (s *Storage) LatestEventNID() (int64, error) {
	return s.accumulator.eventsTable.SelectHighestNID()
}

func (s *Storage) LatestTypingID() (int64, error) {
	return s.TypingTable.SelectHighestID()
}

func (s *Storage) Accumulate(roomID string, timeline []json.RawMessage) (int, error) {
	return s.accumulator.Accumulate(roomID, timeline)
}

func (s *Storage) Initialise(roomID string, state []json.RawMessage) (bool, error) {
	return s.accumulator.Initialise(roomID, state)
}

func (s *Storage) AllJoinedMembers() (map[string][]string, error) {
	// TODO
	_, err := s.accumulator.snapshotTable.CurrentSnapshots()
	if err != nil {
		return nil, err
	}
	return nil, nil
}
