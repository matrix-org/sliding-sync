package testutils

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"os/user"
)

var Quiet = false

func createLocalDB(dbName string) string {
	if !Quiet {
		fmt.Println("Note: tests require a postgres install accessible to the current user")
	}
	createDB := exec.Command("createdb", dbName)
	if !Quiet {
		createDB.Stdout = os.Stdout
		createDB.Stderr = os.Stderr
	}
	createDB.Run()
	return dbName
}

func currentUser() string {
	user, err := user.Current()
	if err != nil {
		if !Quiet {
			fmt.Println("cannot get current user: ", err)
		}
		os.Exit(2)
	}
	return user.Username
}

func PrepareDBConnectionString() (connStr string) {
	// Required vars: user and db
	// We'll try to infer from the local env if they are missing
	user := os.Getenv("POSTGRES_USER")
	if user == "" {
		user = currentUser()
	}
	dbName := os.Getenv("POSTGRES_DB")
	if dbName == "" {
		dbName = createLocalDB("syncv3_test")
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

	// Drop all tables on the database to get a fresh instance
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		panic(err)
	}
	_, err = db.Exec(`DROP SCHEMA public CASCADE;
		CREATE SCHEMA public;`)
	if err != nil {
		panic(err)
	}
	db.Close()
	return
}
