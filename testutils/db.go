package testutils

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
)

func createLocalDB(dbName string) string {
	fmt.Println("Note: tests require a postgres install accessible to the current user")
	dropDB := exec.Command("dropdb", "-f", dbName)
	dropDB.Stdout = os.Stdout
	dropDB.Stderr = os.Stderr
	dropDB.Run()
	createDB := exec.Command("createdb", dbName)
	createDB.Stdout = os.Stdout
	createDB.Stderr = os.Stderr
	if err := createDB.Run(); err != nil {
		fmt.Println("createdb failed: ", err)
		os.Exit(2)
	}
	return dbName
}

func currentUser() string {
	user, err := user.Current()
	if err != nil {
		fmt.Println("cannot get current user: ", err)
		os.Exit(2)
	}
	return user.Username
}

func PrepareDBConnectionString(wantDBName string) (connStr string) {
	// Required vars: user and db
	// We'll try to infer from the local env if they are missing
	user := os.Getenv("POSTGRES_USER")
	if user == "" {
		user = currentUser()
	}
	dbName := os.Getenv("POSTGRES_DB")
	if dbName == "" {
		dbName = createLocalDB(wantDBName)
	}
	connStr = fmt.Sprintf(
		"user=%s dbname=%s sslmode=disable",
		user, dbName,
	)
	// optional vars, used in CI
	password := os.Getenv("POSTGRES_PASSWORD")
	if password != "" {
		connStr += fmt.Sprintf(" password=%s", password)
	}
	host := os.Getenv("POSTGRES_HOST")
	if host != "" {
		connStr += fmt.Sprintf(" host=%s", host)
	}
	return
}
