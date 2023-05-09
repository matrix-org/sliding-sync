package main

import (
	"fmt"
	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	syncv3 "github.com/matrix-org/sliding-sync"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var GitCommit string

const version = "0.99.2"

const (
	// Required fields
	EnvServer = "SYNCV3_SERVER"
	EnvDB     = "SYNCV3_DB"
	EnvSecret = "SYNCV3_SECRET"

	// Optional fields
	EnvBindAddr   = "SYNCV3_BINDADDR"
	EnvTLSCert    = "SYNCV3_TLS_CERT"
	EnvTLSKey     = "SYNCV3_TLS_KEY"
	EnvPPROF      = "SYNCV3_PPROF"
	EnvPrometheus = "SYNCV3_PROM"
	EnvDebug      = "SYNCV3_DEBUG"
	EnvJaeger     = "SYNCV3_JAEGER_URL"
	EnvSentryDsn  = "SYNCV3_SENTRY_DSN"
	EnvLogLevel   = "SYNCV3_LOG_LEVEL"
)

var helpMsg = fmt.Sprintf(`
Environment var
%s     Required. The destination homeserver to talk to (CS API HTTPS URL) e.g 'https://matrix-client.matrix.org'
%s         Required. The postgres connection string: https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING
%s     Required. A secret to use to encrypt access tokens. Must remain the same for the lifetime of the database.
%s   Default: 0.0.0.0:8008.  The interface and port to listen on.
%s   Default: unset. Path to a certificate file to serve to HTTPS clients. Specifying this enables TLS on the bound address.
%s    Default: unset. Path to a key file for the certificate. Must be provided along with the certificate file.
%s      Default: unset. The bind addr for pprof debugging e.g ':6060'. If not set, does not listen.
%s       Default: unset. The bind addr for Prometheus metrics, which will be accessible at /metrics at this address.
%s Default: unset. The Jaeger URL to send spans to e.g http://localhost:14268/api/traces - if unset does not send OTLP traces.
%s Default: unset. The Sentry DSN to report events to e.g https://sliding-sync@sentry.example.com/123 - if unset does not send sentry events.
%s  Default: info. The level of verbosity for messages logged. Available values are trace, debug, info, warn, error and fatal
`, EnvServer, EnvDB, EnvSecret, EnvBindAddr, EnvTLSCert, EnvTLSKey, EnvPPROF, EnvPrometheus, EnvJaeger, EnvSentryDsn, EnvLogLevel)

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
		EnvTLSCert:    os.Getenv(EnvTLSCert),
		EnvTLSKey:     os.Getenv(EnvTLSKey),
		EnvPPROF:      os.Getenv(EnvPPROF),
		EnvPrometheus: os.Getenv(EnvPrometheus),
		EnvDebug:      os.Getenv(EnvDebug),
		EnvJaeger:     os.Getenv(EnvJaeger),
		EnvSentryDsn:  os.Getenv(EnvSentryDsn),
		EnvLogLevel:   os.Getenv(EnvLogLevel),
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
	if (args[EnvTLSCert] != "" || args[EnvTLSKey] != "") && (args[EnvTLSCert] == "" || args[EnvTLSKey] == "") {
		fmt.Print(helpMsg)
		fmt.Printf("\nboth %s and %s must be set together\n", EnvTLSCert, EnvTLSKey)
		os.Exit(1)
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

	// Initialise sentry. We do this in a separate block to the sentry code below,
	// because we want to configure logging before the call to syncv3.Setup---which may
	// want to log to sentry itself.
	if args[EnvSentryDsn] != "" {
		fmt.Printf("Configuring Sentry reporter...\n")
		err := sentry.Init(sentry.ClientOptions{
			Dsn:     args[EnvSentryDsn],
			Release: version,
			Dist:    GitCommit,
		})
		if err != nil {
			panic(err)
		}
	}

	if args[EnvDebug] == "1" {
		zerolog.SetGlobalLevel(zerolog.TraceLevel)
	} else {
		switch strings.ToLower(args[EnvLogLevel]) {
		case "trace":
			zerolog.SetGlobalLevel(zerolog.TraceLevel)
		case "debug":
			zerolog.SetGlobalLevel(zerolog.DebugLevel)
		case "info":
			zerolog.SetGlobalLevel(zerolog.InfoLevel)
		case "warn":
			zerolog.SetGlobalLevel(zerolog.WarnLevel)
		case "err", "error":
			zerolog.SetGlobalLevel(zerolog.ErrorLevel)
		case "fatal":
			zerolog.SetGlobalLevel(zerolog.FatalLevel)
		default:
			zerolog.SetGlobalLevel(zerolog.InfoLevel)
		}
	}

	h2, h3 := syncv3.Setup(args[EnvServer], args[EnvDB], args[EnvSecret], syncv3.Opts{
		AddPrometheusMetrics: args[EnvPrometheus] != "",
	})

	go h2.StartV2Pollers()
	if args[EnvJaeger] != "" {
		h3 = otelhttp.NewHandler(h3, "Sync")
	}

	// Install the Sentry middleware, if configured.
	if args[EnvSentryDsn] != "" {
		sentryHandler := sentryhttp.New(sentryhttp.Options{
			Repanic: true,
		})
		h3 = sentryHandler.Handle(h3)
	}

	syncv3.RunSyncV3Server(h3, args[EnvBindAddr], args[EnvServer], args[EnvTLSCert], args[EnvTLSKey])
	WaitForShutdown(args[EnvSentryDsn] != "")
}

// WaitForShutdown blocks until the process receives a SIGINT or SIGTERM signal
// (see `man 7 signal`). It performs any last cleanup tasks and then exits.
func WaitForShutdown(sentryInUse bool) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sigs:
	}
	signal.Reset(syscall.SIGINT, syscall.SIGTERM)

	fmt.Printf("Shutdown signal received...")

	if sentryInUse {
		fmt.Printf("Flushing sentry events...")
		if !sentry.Flush(time.Second * 5) {
			fmt.Printf("Failed to flush all Sentry events!")
		}
	}

	fmt.Printf("Exiting now")
}
