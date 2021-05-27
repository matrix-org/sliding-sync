package state

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/rs/zerolog"
)

var log = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

// Accumulator tracks room state and timelines.
//
// In order for it to remain simple(ish), the accumulator DOES NOT SUPPORT arbitrary timeline gaps.
// There is an Initialise function for new rooms (with some pre-determined state) and then a constant
// Accumulate function for timeline events. v2 sync must be called with a large enough timeline.limit
// for this to work!
type Accumulator struct {
	mu *sync.Mutex // lock for locks
	// TODO: unbounded on number of rooms
	locks map[string]*sync.Mutex // room_id -> mutex

	db                    *sqlx.DB
	roomsTable            *RoomsTable
	eventsTable           *EventTable
	snapshotTable         *SnapshotTable
	snapshotRefCountTable *SnapshotRefCountsTable
}

func NewAccumulator(postgresURI string) *Accumulator {
	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		log.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}
	return &Accumulator{
		db:                    db,
		mu:                    &sync.Mutex{},
		locks:                 make(map[string]*sync.Mutex),
		roomsTable:            NewRoomsTable(db),
		eventsTable:           NewEventTable(db),
		snapshotTable:         NewSnapshotsTable(db),
		snapshotRefCountTable: NewSnapshotRefCountsTable(db),
	}
}

// obtain a per-room lock
func (a *Accumulator) mutex(roomID string) *sync.Mutex {
	a.mu.Lock()
	defer a.mu.Unlock()
	lock, ok := a.locks[roomID]
	if !ok {
		lock = &sync.Mutex{}
		a.locks[roomID] = lock
	}
	return lock
}

// clearSnapshots deletes all snapshots with 0 refs to it
func (a *Accumulator) clearSnapshots(txn *sqlx.Tx) {
	snapshotIDs, err := a.snapshotRefCountTable.DeleteEmptyRefs(txn)
	if err != nil {
		log.Err(err).Msg("failed to DeleteEmptyRefs")
		return
	}
	err = a.snapshotTable.Delete(txn, snapshotIDs)
	if err != nil {
		log.Err(err).Ints("snapshots", snapshotIDs).Msg("failed to delete snapshot IDs")
	}
}

// moveSnapshotRef decrements the from snapshot ID and increments the to snapshot ID
// Clears the from snapshot if there are 0 refs to it
func (a *Accumulator) moveSnapshotRef(txn *sqlx.Tx, from, to int) error {
	if from != 0 {
		count, err := a.snapshotRefCountTable.Decrement(txn, from)
		if err != nil {
			return err
		}
		if count == 0 {
			a.clearSnapshots(txn)
		}
	}
	_, err := a.snapshotRefCountTable.Increment(txn, to)
	return err
}

// Initialise starts a new sync accumulator for the given room using the given state as a baseline.
// This will only take effect if this is the first time the v3 server has seen this room, and it wasn't
// possible to get all events up to the create event (e.g Matrix HQ).
//
// This function:
// - Stores these events
// - Sets up the current snapshot based on the state list given.
func (a *Accumulator) Initialise(roomID string, state []json.RawMessage) error {
	if len(state) == 0 {
		return nil
	}
	return WithTransaction(a.db, func(txn *sqlx.Tx) error {
		// Attempt to short-circuit. This has to be done inside a transaction to make sure
		// we don't race with multiple calls to Initialise with the same room ID.
		snapshotID, err := a.roomsTable.CurrentSnapshotID(txn, roomID)
		if err != nil {
			return fmt.Errorf("error fetching snapshot id for room %s: %s", roomID, err)
		}
		if snapshotID > 0 {
			// we only initialise rooms once
			log.Info().Str("room_id", roomID).Int("snapshot_id", snapshotID).Msg("Accumulator.Initialise called but current snapshot already exists, bailing early")
			return nil
		}

		// Insert the events
		events := make([]Event, len(state))
		for i := range events {
			events[i] = Event{
				JSON: state[i],
			}
		}
		numNew, err := a.eventsTable.Insert(txn, events)
		if err != nil {
			return err
		}
		if numNew == 0 {
			// we don't have a current snapshot for this room but yet no events are new,
			// no idea how this should be handled.
			log.Error().Str("room_id", roomID).Msg(
				"Accumulator.Initialise: room has no current snapshot but also no new inserted events, doing nothing. This is probably a bug.",
			)
			return nil
		}

		// pull out the event NIDs we just inserted
		eventIDs := make([]string, len(events))
		for i := range eventIDs {
			eventIDs[i] = events[i].ID
		}
		nids, err := a.eventsTable.SelectNIDsByIDs(txn, eventIDs)
		if err != nil {
			return err
		}

		// Make a current snapshot
		snapshot := &SnapshotRow{
			RoomID: roomID,
			Events: pq.Int64Array(nids),
		}
		err = a.snapshotTable.Insert(txn, snapshot)
		if err != nil {
			return err
		}

		// Increment the ref counter
		err = a.moveSnapshotRef(txn, 0, snapshot.SnapshotID)
		if err != nil {
			return err
		}

		// Set the snapshot ID as the current state
		return a.roomsTable.UpdateCurrentSnapshotID(txn, roomID, snapshot.SnapshotID)
	})
}

// Accumulate internal state from a user's sync response. Locks per-room.
//
// This function does several things:
// - It ensures all events are persisted in the database. This is shared amongst users.
// - If the last event in the timeline has been stored before, then it short circuits and returns.
//   This is because we must have already processed this in order for the event to exist in the database,
//   and the sync stream is already linearised for us.
// - Else it creates a new room state snapshot (as this now represents the current state)
// - It checks if there are outstanding references for the previous snapshot, and if not, removes the old snapshot from the database.
//   References are made when clients have synced up to a given snapshot (hence may paginate at that point).
func (a *Accumulator) Accumulate(roomID string, timeline []json.RawMessage) error {
	if len(timeline) == 0 {
		return nil
	}
	return WithTransaction(a.db, func(txn *sqlx.Tx) error {
		return nil
	})
}

// WithTransaction runs a block of code passing in an SQL transaction
// If the code returns an error or panics then the transactions is rolled back
// Otherwise the transaction is committed.
func WithTransaction(db *sqlx.DB, fn func(txn *sqlx.Tx) error) (err error) {
	txn, err := db.Beginx()
	if err != nil {
		return fmt.Errorf("WithTransaction.Begin: %w", err)
	}

	defer func() {
		panicErr := recover()
		if err == nil && panicErr != nil {
			err = fmt.Errorf("panic: %v", panicErr)
		}
		var txnErr error
		if err != nil {
			txnErr = txn.Rollback()
		} else {
			txnErr = txn.Commit()
		}
		if txnErr != nil && err == nil {
			err = fmt.Errorf("WithTransaction failed to commit/rollback: %w", txnErr)
		}
	}()

	err = fn(txn)
	return
}
