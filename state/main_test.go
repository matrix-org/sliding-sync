package state

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"testing"
)

var postgresConnectionString = "user=xxxxx dbname=syncv3_test sslmode=disable"

func TestMain(m *testing.M) {
	fmt.Println("Note: tests require a postgres install accessible to the current user")
	dbName := "syncv3_test"
	// cleanup if we died mid-way last time, hence don't check err output in case it was already deleted
	exec.Command("dropdb", dbName).Run()

	user, err := user.Current()
	if err != nil {
		fmt.Println("cannot get current user: ", err)
		os.Exit(2)
	}
	postgresConnectionString = fmt.Sprintf(
		"user=%s dbname=%s sslmode=disable",
		user.Username, dbName,
	)

	if err := exec.Command("createdb", dbName).Run(); err != nil {
		fmt.Println("createdb failed: ", err)
		os.Exit(2)
	}

	exitCode := m.Run()

	// cleanup
	fmt.Println("cleaning up database")
	exec.Command("dropdb", dbName).Run()
	os.Exit(exitCode)
}
