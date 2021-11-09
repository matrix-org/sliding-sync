package sync3

import (
	"os"
	"testing"

	"github.com/matrix-org/sync-v3/testutils"
	"github.com/rs/zerolog"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		NoColor:    true,
	})
	postgresConnectionString = testutils.PrepareDBConnectionString()
	exitCode := m.Run()
	os.Exit(exitCode)
}
