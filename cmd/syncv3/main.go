package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"time"

	syncv3 "github.com/matrix-org/sync-v3"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3/handler"
)

var GitCommit string

const version = "0.3.1"

const (
	EnvServer   = "SYNCV3_SERVER"
	EnvDB       = "SYNCV3_DB"
	EnvBindAddr = "SYNCV3_BINDADDR"
	EnvSecret   = "SYNCV3_SECRET"
)

var helpMsg = fmt.Sprintf(`
Environment var
%s   Required. The destination homeserver to talk to (CS API HTTPS URL) e.g 'https://matrix-client.matrix.org'
%s       Required. The postgres connection string: https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING 
%s (Default: 0.0.0.0:8008) The interface and port to listen on.
%s   Required. A secret to use to encrypt access tokens. Must remain the same for the lifetime of the database. 
`, EnvServer, EnvDB, EnvBindAddr, EnvSecret)

func defaulting(in, dft string) string {
	if in == "" {
		return dft
	}
	return in
}

func main() {
	fmt.Printf("Sync v3 [%s] (%s)\n", version, GitCommit)
	syncv3.Version = fmt.Sprintf("%s (%s)", version, GitCommit)
	flagDestinationServer := os.Getenv(EnvServer)
	flagPostgres := os.Getenv(EnvDB)
	flagSecret := os.Getenv(EnvSecret)
	flagBindAddr := defaulting(os.Getenv(EnvBindAddr), "0.0.0.0:8008")
	if flagDestinationServer == "" || flagPostgres == "" || flagSecret == "" {
		fmt.Print(helpMsg)
		fmt.Printf("\n%s and %s and %s must be set\n", EnvServer, EnvBindAddr, EnvSecret)
		os.Exit(1)
	}
	// pprof
	go func() {
		if err := http.ListenAndServe(":6060", nil); err != nil {
			panic(err)
		}
	}()
	h, err := handler.NewSync3Handler(&sync2.HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: flagDestinationServer,
	}, flagPostgres, flagSecret, os.Getenv("SYNCV3_DEBUG") == "1")
	if err != nil {
		panic(err)
	}
	go h.StartV2Pollers()
	syncv3.RunSyncV3Server(h, flagBindAddr, flagDestinationServer)
}
