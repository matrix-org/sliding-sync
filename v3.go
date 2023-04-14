package slidingsync

import (
	"encoding/json"
	"fmt"
	"github.com/getsentry/sentry-go"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/pubsub"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync2/handler2"
	"github.com/matrix-org/sliding-sync/sync3/handler"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})
var Version string

type Opts struct {
	Debug                bool
	AddPrometheusMetrics bool
	// The max number of events the client is eligible to read (unfiltered) which we are willing to
	// buffer on this connection. Too large and we consume lots of memory. Too small and busy accounts
	// will trip the connection knifing. Customisable as tests might want to test filling the buffer.
	MaxPendingEventUpdates int
	// if true, publishing messages will block until the consumer has consumed it.
	// Assumes a single producer and a single consumer.
	TestingSynchronousPubsub bool
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
	v2Client := &sync2.HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: destHomeserver,
	}
	store := state.NewStorage(postgresURI)
	storev2 := sync2.NewStore(postgresURI, secret)
	bufferSize := 50
	if opts.TestingSynchronousPubsub {
		bufferSize = 0
	}
	if opts.MaxPendingEventUpdates == 0 {
		opts.MaxPendingEventUpdates = 2000
	}
	pubSub := pubsub.NewPubSub(bufferSize)

	// create v2 handler
	h2, err := handler2.NewHandler(postgresURI, sync2.NewPollerMap(v2Client, opts.AddPrometheusMetrics), storev2, store, v2Client, pubSub, pubSub, opts.AddPrometheusMetrics)
	if err != nil {
		panic(err)
	}

	// create v3 handler
	h3, err := handler.NewSync3Handler(store, storev2, v2Client, postgresURI, secret, opts.Debug, pubSub, pubSub, opts.AddPrometheusMetrics, opts.MaxPendingEventUpdates)
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
				entry.Int("status", status).
					Int("size", size).
					Dur("duration", duration).
					Msg("")
			}),
		},
		final: r,
	}

	// Block forever
	var err error
	if tlsCert != "" && tlsKey != "" {
		logger.Info().Msgf("listening TLS on %s", bindAddr)
		err = http.ListenAndServeTLS(bindAddr, tlsCert, tlsKey, srv)
	} else {
		logger.Info().Msgf("listening on %s", bindAddr)
		err = http.ListenAndServe(bindAddr, srv)
	}
	if err != nil {
		sentry.CaptureException(err)
		// TODO: Fatal() calls os.Exit. Will that give time for sentry.Flush() to run?
		logger.Fatal().Err(err).Msg("failed to listen and serve")
	}
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
