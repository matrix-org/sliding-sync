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
