package state

import (
	"os"
	"testing"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/testutils"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	postgresConnectionString = testutils.PrepareDBConnectionString()
	exitCode := m.Run()
	os.Exit(exitCode)
}

func connectToDB(t *testing.T) (*sqlx.DB, func()) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	return db, func() {
		db.Close()
	}
}
