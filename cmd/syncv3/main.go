package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	sentryhttp "github.com/getsentry/sentry-go/http"
	"github.com/pressly/goose/v3"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	syncv3 "github.com/matrix-org/sliding-sync"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync2"
)

var GitCommit string

const version = "0.99.18"

var (
	flags = flag.NewFlagSet("goose", flag.ExitOnError)
	flagConf = flag.String("conf", "", "path to an optional configuration file.")
)

const (
	// Required fields
	EnvServer = "SYNCV3_SERVER"
	EnvDB     = "SYNCV3_DB"
	EnvSecret = "SYNCV3_SECRET"

	// Optional fields
	EnvBindAddr               = "SYNCV3_BINDADDR"
	EnvTLSCert                = "SYNCV3_TLS_CERT"
	EnvTLSKey                 = "SYNCV3_TLS_KEY"
	EnvPPROF                  = "SYNCV3_PPROF"
	EnvPrometheus             = "SYNCV3_PROM"
	EnvDebug                  = "SYNCV3_DEBUG"
	EnvOTLP                   = "SYNCV3_OTLP_URL"
	EnvOTLPUsername           = "SYNCV3_OTLP_USERNAME"
	EnvOTLPPassword           = "SYNCV3_OTLP_PASSWORD"
	EnvSentryDsn              = "SYNCV3_SENTRY_DSN"
	EnvLogLevel               = "SYNCV3_LOG_LEVEL"
	EnvMaxConns               = "SYNCV3_MAX_DB_CONN"
	EnvIdleTimeoutSecs        = "SYNCV3_DB_IDLE_TIMEOUT_SECS"
	EnvHTTPTimeoutSecs        = "SYNCV3_HTTP_TIMEOUT_SECS"
	EnvHTTPInitialTimeoutSecs = "SYNCV3_HTTP_INITIAL_TIMEOUT_SECS"
)

var helpMsg = fmt.Sprintf(`
Environment var
%s     Required. The destination homeserver to talk to (CS API HTTPS URL) e.g 'https://matrix-client.matrix.org' (Supports unix socket: /path/to/socket)
%s         Required. The postgres connection string: https://www.postgresql.org/docs/current/libpq-connect.html#LIBPQ-CONNSTRING
%s     Required. A secret to use to encrypt access tokens. Must remain the same for the lifetime of the database.
%s   Default: 0.0.0.0:8008.  The interface and port to listen on. (Supports unix socket: /path/to/socket)
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
%s Default: 3600. The maximum amount of time a database connection may be idle, in seconds. 0 means no limit.
%s Default: 300. The timeout in seconds for normal HTTP requests.
%s Default: 1800. The timeout in seconds for initial sync requests.
`, EnvServer, EnvDB, EnvSecret, EnvBindAddr, EnvTLSCert, EnvTLSKey, EnvPPROF, EnvPrometheus, EnvOTLP, EnvOTLPUsername, EnvOTLPPassword,
	EnvSentryDsn, EnvLogLevel, EnvMaxConns, EnvIdleTimeoutSecs, EnvHTTPTimeoutSecs, EnvHTTPInitialTimeoutSecs)

func defaulting(in, dft string) string {
	if in == "" {
		return dft
	}
	return in
}

func readConf(filePath string) (map[string]string, error) {
	conf := map[string]string{}

	if filePath == "" {
		return conf, nil
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return conf, err
	}

	if err := json.Unmarshal(content, &conf); err != nil {
		return conf, err
	}

	return conf, nil
}

func main() {
	fmt.Printf("Sync v3 [%s] (%s)\n", version, GitCommit)
	sync2.ProxyVersion = version
	syncv3.Version = fmt.Sprintf("%s (%s)", version, GitCommit)

	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		executeMigrations()
		return
	}

	flag.Parse()
	confArgs, err := readConf(*flagConf)
	if err != nil {
		log.Fatalf("failed to read configuration file: %v\n", err)
	}

	args := map[string]string{
		EnvServer:                 defaulting(os.Getenv(EnvServer), confArgs[EnvServer]),
		EnvDB:                     defaulting(os.Getenv(EnvDB), confArgs[EnvDB]),
		EnvSecret:                 defaulting(os.Getenv(EnvSecret), confArgs[EnvSecret]),
		EnvBindAddr:               defaulting(os.Getenv(EnvBindAddr), defaulting(confArgs[EnvBindAddr], "0.0.0.0:8008")),
		EnvTLSCert:                defaulting(os.Getenv(EnvTLSCert), confArgs[EnvTLSCert]),
		EnvTLSKey:                 defaulting(os.Getenv(EnvTLSKey), confArgs[EnvTLSKey]),
		EnvPPROF:                  defaulting(os.Getenv(EnvPPROF), confArgs[EnvPPROF]),
		EnvPrometheus:             defaulting(os.Getenv(EnvPrometheus), confArgs[EnvPrometheus]),
		EnvDebug:                  defaulting(os.Getenv(EnvDebug), confArgs[EnvDebug]),
		EnvOTLP:                   defaulting(os.Getenv(EnvOTLP), confArgs[EnvOTLP]),
		EnvOTLPUsername:           defaulting(os.Getenv(EnvOTLPUsername), confArgs[EnvOTLPUsername]),
		EnvOTLPPassword:           defaulting(os.Getenv(EnvOTLPPassword), confArgs[EnvOTLPPassword]),
		EnvSentryDsn:              defaulting(os.Getenv(EnvSentryDsn), confArgs[EnvSentryDsn]),
		EnvLogLevel:               defaulting(os.Getenv(EnvLogLevel), confArgs[EnvLogLevel]),
		EnvMaxConns:               defaulting(os.Getenv(EnvMaxConns), defaulting(confArgs[EnvMaxConns], "0")),
		EnvIdleTimeoutSecs:        defaulting(os.Getenv(EnvIdleTimeoutSecs), defaulting(confArgs[EnvIdleTimeoutSecs], "3600")),
		EnvHTTPTimeoutSecs:        defaulting(os.Getenv(EnvHTTPTimeoutSecs), defaulting(confArgs[EnvHTTPTimeoutSecs], "300")),
		EnvHTTPInitialTimeoutSecs: defaulting(os.Getenv(EnvHTTPInitialTimeoutSecs), defaulting(confArgs[EnvHTTPInitialTimeoutSecs], "1800")),
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

	maxConnsInt, err := strconv.Atoi(args[EnvMaxConns])
	if err != nil {
		panic("invalid value for " + EnvMaxConns + ": " + args[EnvMaxConns])
	}
	idleTimeSecs, err := strconv.Atoi(args[EnvIdleTimeoutSecs])
	if err != nil {
		panic("invalid value for " + EnvIdleTimeoutSecs + ": " + args[EnvIdleTimeoutSecs])
	}
	httpTimeoutSecs, err := strconv.Atoi(args[EnvHTTPTimeoutSecs])
	if err != nil {
		panic("invalid value for " + EnvHTTPTimeoutSecs + ": " + args[EnvHTTPTimeoutSecs])
	}
	httpLongTimeoutSecs, err := strconv.Atoi(args[EnvHTTPInitialTimeoutSecs])
	if err != nil {
		panic("invalid value for " + EnvHTTPInitialTimeoutSecs + ": " + args[EnvHTTPInitialTimeoutSecs])
	}
	h2, h3 := syncv3.Setup(args[EnvServer], args[EnvDB], args[EnvSecret], syncv3.Opts{
		AddPrometheusMetrics:  args[EnvPrometheus] != "",
		DBMaxConns:            maxConnsInt,
		DBConnMaxIdleTime:     time.Duration(idleTimeSecs) * time.Second,
		MaxTransactionIDDelay: time.Second,
		HTTPTimeout:           time.Duration(httpTimeoutSecs) * time.Second,
		HTTPLongTimeout:       time.Duration(httpLongTimeoutSecs) * time.Second,
	})

	go h2.StartV2Pollers()
	go h2.Store.Cleaner(time.Hour)
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

const gitRevLen = 7 // 7 matches the displayed characters on github.com
func init() {
	// Try to get the revision sliding-sync was build from.
	// If we can't, e.g. sliding-sync wasn't built (go run) or no VCS version is present,
	// we just use the provided version above.
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}

	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			revLen := len(setting.Value)
			if revLen >= gitRevLen {
				GitCommit = setting.Value[:gitRevLen]
			} else {
				GitCommit = setting.Value[:revLen]
			}
			break
		}
	}
}
