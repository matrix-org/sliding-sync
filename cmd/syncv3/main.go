package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	syncv3 "github.com/matrix-org/sliding-sync"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

var GitCommit string

const version = "0.99.8"

var (
	flags = flag.NewFlagSet("goose", flag.ExitOnError)
)

const (
	// Required fields
	EnvServer = "SYNCV3_SERVER"
	EnvDB     = "SYNCV3_DB"
	EnvSecret = "SYNCV3_SECRET"

	// Optional fields
	EnvBindAddr     = "SYNCV3_BINDADDR"
	EnvTLSCert      = "SYNCV3_TLS_CERT"
	EnvTLSKey       = "SYNCV3_TLS_KEY"
	EnvPPROF        = "SYNCV3_PPROF"
	EnvPrometheus   = "SYNCV3_PROM"
	EnvDebug        = "SYNCV3_DEBUG"
	EnvOTLP         = "SYNCV3_OTLP_URL"
	EnvOTLPUsername = "SYNCV3_OTLP_USERNAME"
	EnvOTLPPassword = "SYNCV3_OTLP_PASSWORD"
	EnvSentryDsn    = "SYNCV3_SENTRY_DSN"
	EnvLogLevel     = "SYNCV3_LOG_LEVEL"
	EnvMaxConns     = "SYNCV3_MAX_DB_CONN"
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
%s Default: unset. The OTLP HTTP URL to send spans to e.g https://localhost:4318 - if unset does not send OTLP traces.
%s Default: unset. The OTLP username for Basic auth. If unset, does not send an Authorization header.
%s Default: unset. The OTLP password for Basic auth. If unset, does not send an Authorization header.
%s Default: unset. The Sentry DSN to report events to e.g https://sliding-sync@sentry.example.com/123 - if unset does not send sentry events.
%s  Default: info. The level of verbosity for messages logged. Available values are trace, debug, info, warn, error and fatal
%s Default: unset. Max database connections to use when communicating with postgres. Unset or 0 means no limit.
`, EnvServer, EnvDB, EnvSecret, EnvBindAddr, EnvTLSCert, EnvTLSKey, EnvPPROF, EnvPrometheus, EnvOTLP, EnvOTLPUsername, EnvOTLPPassword,
	EnvSentryDsn, EnvLogLevel, EnvMaxConns)

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

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		executeMigrations()
		return
	}

	args := map[string]string{
		EnvServer:       os.Getenv(EnvServer),
		EnvDB:           os.Getenv(EnvDB),
		EnvSecret:       os.Getenv(EnvSecret),
		EnvBindAddr:     defaulting(os.Getenv(EnvBindAddr), "0.0.0.0:8008"),
		EnvTLSCert:      os.Getenv(EnvTLSCert),
		EnvTLSKey:       os.Getenv(EnvTLSKey),
		EnvPPROF:        os.Getenv(EnvPPROF),
		EnvPrometheus:   os.Getenv(EnvPrometheus),
		EnvDebug:        os.Getenv(EnvDebug),
		EnvOTLP:         os.Getenv(EnvOTLP),
		EnvOTLPUsername: os.Getenv(EnvOTLPUsername),
		EnvOTLPPassword: os.Getenv(EnvOTLPPassword),
		EnvSentryDsn:    os.Getenv(EnvSentryDsn),
		EnvLogLevel:     os.Getenv(EnvLogLevel),
		EnvMaxConns:     defaulting(os.Getenv(EnvMaxConns), "0"),
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
	if args[EnvOTLP] != "" {
		fmt.Printf("Configuring OTLP collector...\n")
		if err := internal.ConfigureOTLP(args[EnvOTLP], args[EnvOTLPUsername], args[EnvOTLPPassword], syncv3.Version); err != nil {
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

	fmt.Printf("Debug=%v LogLevel=%v MaxConns=%v\n", args[EnvDebug] == "1", args[EnvLogLevel], args[EnvMaxConns])

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

	err := sync2.MigrateDeviceIDs(args[EnvServer], args[EnvDB], args[EnvSecret], true)
	if err != nil {
		panic(err)
	}

	maxConnsInt, err := strconv.Atoi(args[EnvMaxConns])
	if err != nil {
		panic("invalid value for " + EnvMaxConns + ": " + args[EnvMaxConns])
	}
	h2, h3 := syncv3.Setup(args[EnvServer], args[EnvDB], args[EnvSecret], syncv3.Opts{
		AddPrometheusMetrics:  args[EnvPrometheus] != "",
		DBMaxConns:            maxConnsInt,
		DBConnMaxIdleTime:     time.Hour,
		MaxTransactionIDDelay: time.Second,
	})

	go h2.StartV2Pollers()
	if args[EnvOTLP] != "" {
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

func executeMigrations() {
	envArgs := map[string]string{
		EnvDB: os.Getenv(EnvDB),
	}
	requiredEnvVars := []string{EnvDB}
	for _, requiredEnvVar := range requiredEnvVars {
		if envArgs[requiredEnvVar] == "" {
			fmt.Print(helpMsg)
			fmt.Printf("\n%s is not set", requiredEnvVar)
			fmt.Printf("\n%s must be set\n", strings.Join(requiredEnvVars, ", "))
			os.Exit(1)
		}
	}

	flags.Parse(os.Args[1:])
	args := flags.Args()

	if len(args) < 2 {
		flags.Usage()
		return
	}

	command := args[1]

	db, err := goose.OpenDBWithDriver("postgres", envArgs[EnvDB])
	if err != nil {
		log.Fatalf("goose: failed to open DB: %v\n", err)
	}

	defer func() {
		if err := db.Close(); err != nil {
			log.Fatalf("goose: failed to close DB: %v\n", err)
		}
	}()

	arguments := []string{}
	if len(args) > 2 {
		arguments = append(arguments, args[2:]...)
	}

	goose.SetBaseFS(syncv3.EmbedMigrations)
	if err := goose.Run(command, db, "state/migrations", arguments...); err != nil {
		log.Fatalf("goose %v: %v", command, err)
	}
}
