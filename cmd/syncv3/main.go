package main

import (
	"flag"
	"os"

	syncv3 "github.com/matrix-org/sync-v3"
)

var (
	flagDestinationServer = flag.String("server", "", "The destination v2 matrix server")
	flagBindAddr          = flag.String("port", ":8008", "Bind address")
	flagPostgres          = flag.String("db", "user=postgres dbname=syncv3 sslmode=disable", "Postgres DB connection string (see lib/pq docs)")
)

func main() {
	flag.Parse()
	if *flagDestinationServer == "" {
		flag.Usage()
		os.Exit(1)
	}
	syncv3.RunSyncV3Server(*flagDestinationServer, *flagBindAddr, *flagPostgres)
}
