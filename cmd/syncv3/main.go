package main

import (
	"flag"
	"net/http"
	"os"
	"time"

	syncv3 "github.com/matrix-org/sync-v3"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/synclive"
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
	syncv3.RunSyncV3Server(synclive.NewSyncLiveHandler(&sync2.HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: *flagDestinationServer,
	}, *flagPostgres), *flagBindAddr)
}
