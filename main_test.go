package syncv3

import (
	"os"
	"testing"

	"github.com/matrix-org/sync-v3/testutils"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	postgresConnectionString = testutils.PrepareDBConnectionString("syncv3_test_main")
	exitCode := m.Run()
	os.Exit(exitCode)
}
