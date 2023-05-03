package sync2

import (
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/matrix-org/sliding-sync/state"
	"testing"
)

func TestNewSchemaIsConsideredMigrated(t *testing.T) {
	NewStore(postgresConnectionString, "my_secret")
	state.NewStorage(postgresConnectionString)

	db, close := connectToDB(t)
	defer close()
	var migrated bool
	err := sqlutil.WithTransaction(db, func(txn *sqlx.Tx) (err error) {
		migrated, err = isMigrated(txn)
		return
	})
	if err != nil {
		t.Fatalf("Error calling isMigrated: %s", err)
	}
	if !migrated {
		t.Fatalf("Expected a new DB to be considered migrated, but it was not")
	}
}
