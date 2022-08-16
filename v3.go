package syncv3

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})
var Version string

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

// RunSyncV3Server is the main entry point to the server
func RunSyncV3Server(h http.Handler, bindAddr, destV2Server string) {
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
	logger.Info().Msgf("listening on %s", bindAddr)
	if err := http.ListenAndServe(bindAddr, srv); err != nil {
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
