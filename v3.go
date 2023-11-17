package slidingsync

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gorilla/mux"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/pubsub"
	"github.com/matrix-org/sliding-sync/state"
	_ "github.com/matrix-org/sliding-sync/state/migrations"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync2/handler2"
	"github.com/matrix-org/sliding-sync/sync3/handler"
	"github.com/pressly/goose/v3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

//go:embed state/migrations/*
var EmbedMigrations embed.FS

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})
var Version string

type Opts struct {
	AddPrometheusMetrics bool
	// The max number of events the client is eligible to read (unfiltered) which we are willing to
	// buffer on this connection. Too large and we consume lots of memory. Too small and busy accounts
	// will trip the connection knifing. Customisable as tests might want to test filling the buffer.
	MaxPendingEventUpdates int
	// if true, publishing messages will block until the consumer has consumed it.
	// Assumes a single producer and a single consumer.
	TestingSynchronousPubsub bool
	// MaxTransactionIDDelay is the longest amount of time that we will wait for
	// confirmation of an event's transaction_id before sending it to its sender.
	// Set to 0 to disable this delay mechanism entirely.
	MaxTransactionIDDelay time.Duration

	DBMaxConns        int
	DBConnMaxIdleTime time.Duration

	// HTTPTimeout is used for "normal" HTTP requests
	HTTPTimeout time.Duration
	// HTTPLongTimeout is used for initial sync requests
	HTTPLongTimeout time.Duration
}

type server struct {
	chain []func(next http.Handler) http.Handler
	final http.Handler
}

func (s *server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	h := s.final
	for i := range s.chain {
		h = s.chain[len(s.chain)-1-i](h)
	}
	h.ServeHTTP(w, req)
}

func allowCORS(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Origin, X-Requested-With, Content-Type, Accept, Authorization")
		if req.Method == "OPTIONS" {
			w.WriteHeader(200)
			return
		}
		next.ServeHTTP(w, req)
	}
}

// Setup the proxy
func Setup(destHomeserver, postgresURI, secret string, opts Opts) (*handler2.Handler, http.Handler) {
	// Setup shared DB and HTTP client
	v2Client := sync2.NewHTTPClient(opts.HTTPTimeout, opts.HTTPLongTimeout, destHomeserver)

	// Sanity check that we can contact the upstream homeserver.
	_, err := v2Client.Versions(context.Background())
	if err != nil {
		logger.Warn().Err(err).Str("dest", destHomeserver).Msg("Could not contact upstream homeserver. Is SYNCV3_SERVER set correctly?")
	}

	db, err := sqlx.Open("postgres", postgresURI)
	if err != nil {
		sentry.CaptureException(err)
		// TODO: if we panic(), will sentry have a chance to flush the event?
		logger.Panic().Err(err).Str("uri", postgresURI).Msg("failed to open SQL DB")
	}

	if opts.DBMaxConns > 0 {
		// https://github.com/go-sql-driver/mysql#important-settings
		// "db.SetMaxIdleConns() is recommended to be set same to db.SetMaxOpenConns(). When it is smaller
		// than SetMaxOpenConns(), connections can be opened and closed much more frequently than you expect."
		db.SetMaxOpenConns(opts.DBMaxConns)
		db.SetMaxIdleConns(opts.DBMaxConns)
	}
	if opts.DBConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(opts.DBConnMaxIdleTime)
	}
	store := state.NewStorageWithDB(db, opts.AddPrometheusMetrics)
	storev2 := sync2.NewStoreWithDB(db, secret)

	// Automatically execute migrations
	goose.SetBaseFS(EmbedMigrations)
	err = goose.Up(db.DB, "state/migrations", goose.WithAllowMissing())
	if err != nil {
		logger.Panic().Err(err).Msg("failed to execute migrations")
	}

	bufferSize := 50
	deviceDataUpdateFrequency := time.Second
	if opts.TestingSynchronousPubsub {
		bufferSize = 0
		deviceDataUpdateFrequency = 0 // don't batch
	}
	if opts.MaxPendingEventUpdates == 0 {
		opts.MaxPendingEventUpdates = 2000
	}
	pubSub := pubsub.NewPubSub(bufferSize)

	pMap := sync2.NewPollerMap(v2Client, opts.AddPrometheusMetrics)
	// create v2 handler
	h2, err := handler2.NewHandler(pMap, storev2, store, pubSub, pubSub, opts.AddPrometheusMetrics, deviceDataUpdateFrequency)
	if err != nil {
		panic(err)
	}
	pMap.SetCallbacks(h2)

	// create v3 handler
	h3, err := handler.NewSync3Handler(store, storev2, v2Client, secret, pubSub, pubSub, opts.AddPrometheusMetrics, opts.MaxPendingEventUpdates, opts.MaxTransactionIDDelay)
	if err != nil {
		panic(err)
	}
	storeSnapshot, err := store.GlobalSnapshot()
	if err != nil {
		panic(err)
	}
	logger.Info().Msg("retrieved global snapshot from database")
	h3.Startup(&storeSnapshot)

	// begin consuming from these positions
	h2.Listen()
	h3.Listen()
	return h2, h3
}

// RunSyncV3Server is the main entry point to the server
func RunSyncV3Server(h http.Handler, bindAddr, destV2Server, tlsCert, tlsKey string) {
	// HTTP path routing
	r := mux.NewRouter()
	r.Handle("/_matrix/client/v3/sync", allowCORS(h))
	r.Handle("/_matrix/client/unstable/org.matrix.msc3575/sync", allowCORS(h))

	serverJSON, _ := json.Marshal(struct {
		Server  string `json:"server"`
		Version string `json:"version"`
	}{
		Server:  destV2Server,
		Version: Version,
	})
	r.Handle("/client/server.json", allowCORS(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(200)
		rw.Write(serverJSON)
	})))
	r.PathPrefix("/client/").HandlerFunc(
		allowCORS(
			http.StripPrefix("/client/", http.FileServer(http.Dir("./client"))),
		),
	)

	srv := &server{
		chain: []func(next http.Handler) http.Handler{
			hlog.NewHandler(logger),
			func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r = r.WithContext(internal.RequestContext(r.Context()))
					next.ServeHTTP(w, r)
				})
			},
			hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
				if r.Method == "OPTIONS" {
					return
				}
				entry := internal.DecorateLogger(r.Context(), hlog.FromRequest(r).Info())
				if !strings.HasSuffix(r.URL.Path, "/sync") {
					entry.Str("path", r.URL.Path)
				}
				durStr := fmt.Sprintf("%.3f", duration.Seconds())
				setupDur, processingDur := internal.RequestContextDurations(r.Context())
				if setupDur != 0 || processingDur != 0 {
					durStr += fmt.Sprintf("(%.3f+%.3f)", setupDur.Seconds(), processingDur.Seconds())
				}
				entry.Int("status", status).
					Int("size", size).
					Str("duration", durStr).
					Msg("")
			}),
		},
		final: r,
	}

	// Block forever
	var err error
	if strings.HasPrefix(bindAddr, "/") {
		logger.Info().Msgf("listening on unix socket %s", bindAddr)
		listener := unixSocketListener(bindAddr)
		err = http.Serve(listener, srv)
	} else {
		if tlsCert != "" && tlsKey != "" {
			logger.Info().Msgf("listening TLS on %s", bindAddr)
			err = http.ListenAndServeTLS(bindAddr, tlsCert, tlsKey, srv)
		} else {
			logger.Info().Msgf("listening on %s", bindAddr)
			err = http.ListenAndServe(bindAddr, srv)
		}
	}
	if err != nil {
		sentry.CaptureException(err)
		// TODO: Fatal() calls os.Exit. Will that give time for sentry.Flush() to run?
		logger.Fatal().Err(err).Msg("failed to listen and serve")
	}
}

func unixSocketListener(bindAddr string) net.Listener {
	err := os.Remove(bindAddr)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		logger.Fatal().Err(err).Msg("failed to remove existing unix socket")
	}
	listener, err := net.Listen("unix", bindAddr)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to serve unix socket")
	}
	// TODO: safe default for now (rwxr-xr-x), could be extracted as env variable if needed
	err = os.Chmod(bindAddr, 0755)
	if err != nil {
		logger.Fatal().Err(err).Msg("failed to set unix socket permissions")
	}
	return listener
}

type HandlerError struct {
	StatusCode int
	Err        error
}

func (e *HandlerError) Error() string {
	return fmt.Sprintf("HTTP %d : %s", e.StatusCode, e.Err.Error())
}

type jsonError struct {
	Err string `json:"error"`
}

func (e HandlerError) JSON() []byte {
	je := jsonError{e.Error()}
	b, _ := json.Marshal(je)
	return b
}
