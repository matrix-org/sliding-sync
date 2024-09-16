package sync3

import (
	"os"
	"testing"

	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
		NoColor:    true,
	})
	postgresConnectionString = testutils.PrepareDBConnectionString()
	exitCode := m.Run()
	os.Exit(exitCode)
}
