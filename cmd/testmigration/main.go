package main

import (
	"github.com/matrix-org/sliding-sync/sync2"
	"os"
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

	err := sync2.MigrateDeviceIDs(args[EnvServer], args[EnvDB], args[EnvSecret], args[EnvMigrationCommit] != "")
	if err != nil {
		panic(err)
	}
}
