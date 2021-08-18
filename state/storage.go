package state

import (
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/tidwall/gjson"
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
		db:                 db,
		roomsTable:         NewRoomsTable(db),
		eventsTable:        NewEventTable(db),
		snapshotTable:      NewSnapshotsTable(db),
		membershipLogTable: NewMembershipLogTable(db),
		entityName:         "server",
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

func (s *Storage) RoomStateAfterEventPosition(roomID string, pos int64) (events []Event, err error) {
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		snapID, err := s.accumulator.eventsTable.AfterEpochSnapshotIDForEventNID(txn, roomID, pos)
		if err != nil {
			return err
		}
		snapshotRow, err := s.accumulator.snapshotTable.Select(txn, snapID)
		if err != nil {
			return err
		}
		events, err = s.accumulator.eventsTable.SelectByNIDs(txn, snapshotRow.Events)
		return err
	})
	return
}

func (s *Storage) RoomMembershipDelta(roomID string, from, to int64, limit int) (eventJSON []json.RawMessage, upTo int64, err error) {
	err = sqlutil.WithTransaction(s.accumulator.db, func(txn *sqlx.Tx) error {
		nids, err := s.accumulator.membershipLogTable.MembershipsBetweenForRoom(txn, from, to, limit, roomID)
		if err != nil {
			return err
		}
		if len(nids) == 0 {
			return nil
		}
		upTo = nids[len(nids)-1]
		events, err := s.accumulator.eventsTable.SelectByNIDs(txn, nids)
		if err != nil {
			return err
		}
		eventJSON = make([]json.RawMessage, len(events))
		for i := range events {
			eventJSON[i] = events[i].JSON
		}
		return nil
	})
	return
}

func (s *Storage) AllJoinedMembers() (map[string][]string, error) {
	roomIDToEventNIDs, err := s.accumulator.snapshotTable.CurrentSnapshots()
	if err != nil {
		return nil, err
	}
	result := make(map[string][]string)
	for roomID, eventNIDs := range roomIDToEventNIDs {
		events, err := s.accumulator.eventsTable.SelectByNIDs(nil, eventNIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to select events in room %s: %s", roomID, err)
		}
		for _, ev := range events {
			evj := gjson.ParseBytes(ev.JSON)
			if evj.Get("type").Str != gomatrixserverlib.MRoomMember {
				continue
			}
			if evj.Get("content.membership").Str != gomatrixserverlib.Join {
				continue
			}
			result[roomID] = append(result[roomID], evj.Get("state_key").Str)
		}
	}
	return result, nil
}

func (s *Storage) JoinedRooms(userID string, pos int64) {

}
