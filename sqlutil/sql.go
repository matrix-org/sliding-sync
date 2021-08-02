package sqlutil

import (
	"fmt"

	"github.com/jmoiron/sqlx"
)

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

type Chunker interface {
	Len() int
	Subslice(i, j int) Chunker
}

// Chunkify will break up things to be inserted based on the number of params in the statement.
// It is required because postgres has a limit on the number of params in a single statement (65535).
// Inserting events using NamedExec involves 3n params (n=number of events), meaning it's easy to hit
// the limit in rooms like Matrix HQ. This function breaks up the events into chunks which can be
// batch inserted in multiple statements. Without this, you'll see errors like:
//     "pq: got 95331 parameters but PostgreSQL only supports 65535 parameters"
func Chunkify(numParamsPerStmt, maxParamsPerCall int, entries Chunker) []Chunker {
	// common case, most things are small
	if (entries.Len() * numParamsPerStmt) <= maxParamsPerCall {
		return []Chunker{
			entries,
		}
	}
	var chunks []Chunker
	// work out how many events can fit in a chunk
	numEntriesPerChunk := (maxParamsPerCall / numParamsPerStmt)
	for i := 0; i < entries.Len(); i += numEntriesPerChunk {
		endIndex := i + numEntriesPerChunk
		if endIndex > entries.Len() {
			endIndex = entries.Len()
		}
		chunks = append(chunks, entries.Subslice(i, endIndex))
	}

	return chunks
}
