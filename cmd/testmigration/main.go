package main

import (
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sync2"
	"net/http"
	"os"
	"time"
)

const (
	// Required fields
	EnvServer = "SYNCV3_SERVER"
	EnvDB     = "SYNCV3_DB"
	EnvSecret = "SYNCV3_SECRET"

	// Migration test only
	EnvMigrationCommit = "SYNCV3_TEST_MIGRATION_COMMIT"
)

func main() {
	args := map[string]string{
		EnvServer:          os.Getenv(EnvServer),
		EnvDB:              os.Getenv(EnvDB),
		EnvSecret:          os.Getenv(EnvSecret),
		EnvMigrationCommit: os.Getenv(EnvMigrationCommit),
	}

	db, err := sqlx.Open("postgres", args[EnvDB])
	if err != nil {
		panic(err)
	}

	v2Client := &sync2.HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: args[EnvServer],
	}

	err = sync2.MigrateDeviceIDs(db, args[EnvDB], v2Client, args[EnvMigrationCommit] != "")
	if err != nil {
		panic(err)
	}
}
