package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"

	syncv3 "github.com/matrix-org/sliding-sync"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

var GitCommit string

const version = "0.99.0"

const (
	// Required fields
	EnvServer = "SYNCV3_SERVER"
	EnvDB     = "SYNCV3_DB"
	EnvSecret = "SYNCV3_SECRET"

	// Optional fields
	EnvBindAddr   = "SYNCV3_BINDADDR"
	EnvPPROF      = "SYNCV3_PPROF"
	EnvPrometheus = "SYNCV3_PROM"
	EnvDebug      = "SYNCV3_DEBUG"
	EnvJaeger     = "SYNCV3_JAEGER_URL"
)

var helpMsg = fmt.Sprintf(`
Environment var
%s   Required. The destination homeserver to talk to (CS API HTTPS URL) e.g 'https://matrix-client.matrix.org'
%s       Required. The postgres connection string: https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING 
%s (Default: 0.0.0.0:8008) The interface and port to listen on.
%s   Required. A secret to use to encrypt access tokens. Must remain the same for the lifetime of the database.
%s   Defualt: unset. The bind addr for pprof debugging e.g ':6060'. If not set, does not listen.
%s   Default: unset. The bind addr for Prometheus metrics, which will be accessible at /metrics at this address.
%s   Default: unset. The Jaeger URL to send spans to e.g http://localhost:14268/api/traces - if unset does not send OTLP traces.
`, EnvServer, EnvDB, EnvBindAddr, EnvSecret, EnvPPROF, EnvPrometheus, EnvJaeger)

func defaulting(in, dft string) string {
	if in == "" {
		return dft
	}
	return in
}

func main() {
	fmt.Printf("Sync v3 [%s] (%s)\n", version, GitCommit)
	sync2.ProxyVersion = version
	syncv3.Version = fmt.Sprintf("%s (%s)", version, GitCommit)
	args := map[string]string{
		EnvServer:     os.Getenv(EnvServer),
		EnvDB:         os.Getenv(EnvDB),
		EnvSecret:     os.Getenv(EnvSecret),
		EnvBindAddr:   defaulting(os.Getenv(EnvBindAddr), "0.0.0.0:8008"),
		EnvPPROF:      os.Getenv(EnvPPROF),
		EnvPrometheus: os.Getenv(EnvPrometheus),
		EnvDebug:      os.Getenv(EnvDebug),
		EnvJaeger:     os.Getenv(EnvJaeger),
	}
	requiredEnvVars := []string{EnvServer, EnvDB, EnvSecret, EnvBindAddr}
	for _, requiredEnvVar := range requiredEnvVars {
		if args[requiredEnvVar] == "" {
			fmt.Print(helpMsg)
			fmt.Printf("\n%s is not set", requiredEnvVar)
			fmt.Printf("\n%s must be set\n", strings.Join(requiredEnvVars, ", "))
			os.Exit(1)
		}
	}
	// pprof
	if args[EnvPPROF] != "" {
		go func() {
			fmt.Printf("Starting pprof listener on %s\n", args[EnvPPROF])
			if err := http.ListenAndServe(args[EnvPPROF], nil); err != nil {
				panic(err)
			}
		}()
	}
	if args[EnvPrometheus] != "" {
		go func() {
			fmt.Printf("Starting prometheus listener on %s\n", args[EnvPrometheus])
			http.Handle("/metrics", promhttp.Handler())
			if err := http.ListenAndServe(args[EnvPrometheus], nil); err != nil {
				panic(err)
			}
		}()
	}
	if args[EnvJaeger] != "" {
		fmt.Printf("Configuring Jaeger collector...\n")
		if err := internal.ConfigureJaeger(args[EnvJaeger], syncv3.Version); err != nil {
			panic(err)
		}
	}
	h2, h3 := syncv3.Setup(args[EnvServer], args[EnvDB], args[EnvSecret], syncv3.Opts{
		Debug:                args[EnvDebug] == "1",
		AddPrometheusMetrics: args[EnvPrometheus] != "",
	})

	go h2.StartV2Pollers()
	if args[EnvJaeger] != "" {
		h3 = otelhttp.NewHandler(h3, "Sync")
	}
	syncv3.RunSyncV3Server(h3, args[EnvBindAddr], args[EnvServer])
	select {} // block forever
}
