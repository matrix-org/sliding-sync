package syncv3

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/streams"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

var log = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

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

// RunSyncV3Server is the main entry point to the server
func RunSyncV3Server(destinationServer, bindAddr, postgresDBURI string) {
	// dependency inject all components together
	sh := &SyncV3Handler{
		V2: &sync2.Client{
			Client: &http.Client{
				Timeout: 5 * time.Minute,
			},
			DestinationServer: destinationServer,
		},
		Sessions: sync3.NewSessions(postgresDBURI),
		Storage:  state.NewStorage(postgresDBURI),
		Pollers:  make(map[string]*sync2.Poller),
		pollerMu: &sync.Mutex{},
	}

	// HTTP path routing
	r := mux.NewRouter()
	r.Handle("/_matrix/client/v3/sync", sh)

	srv := &server{
		chain: []func(next http.Handler) http.Handler{
			hlog.NewHandler(log),
			hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
				hlog.FromRequest(r).Info().
					Str("method", r.Method).
					Int("status", status).
					Int("size", size).
					Dur("duration", duration).
					Str("since", r.URL.Query().Get("since")).
					Msg("")
			}),
			hlog.RemoteAddrHandler("ip"),
		},
		final: r,
	}

	// Block forever
	log.Info().Msgf("listening on %s", bindAddr)
	if err := http.ListenAndServe(bindAddr, srv); err != nil {
		log.Fatal().Err(err).Msg("failed to listen and serve")
	}
}

type handlerError struct {
	StatusCode int
	err        error
}

func (e *handlerError) Error() string {
	return fmt.Sprintf("HTTP %d : %s", e.StatusCode, e.err.Error())
}

type SyncV3Handler struct {
	V2       *sync2.Client
	Sessions *sync3.Sessions
	Storage  *state.Storage

	pollerMu *sync.Mutex
	Pollers  map[string]*sync2.Poller // device_id -> poller
}

func (h *SyncV3Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	err := h.serve(w, req)
	if err != nil {
		w.WriteHeader(err.StatusCode)
		w.Write(asJSONError(err))
	}
}

func (h *SyncV3Handler) serve(w http.ResponseWriter, req *http.Request) *handlerError {
	session, tokv3, herr := h.getOrCreateSession(req)
	if herr != nil {
		return herr
	}
	log.Info().Int64("session", session.ID).Str("device", session.DeviceID).Msg("recv /v3/sync")

	// make sure we have a poller for this device
	h.ensurePolling(req.Header.Get("Authorization"), session)

	// fetch the latest value which we'll base our response on
	latestNID, err := h.Storage.LatestEventNID()
	if err != nil {
		return &handlerError{
			err:        err,
			StatusCode: 500,
		}
	}
	upcoming := sync3.Token{
		SessionID: session.ID,
		NID:       latestNID,
	}
	var from int64
	if tokv3 != nil {
		from = tokv3.NID
	}

	// TODO: read filters
	// TODO: read streams
	f := false
	filter := &streams.FilterRoomList{
		EntriesPerBatch:      5,
		RoomNameSize:         70,
		IncludeRoomAvatarMXC: &f,
		SummaryEventTypes:    []string{"m.room.message", "m.room.member"},
	}
	stream := streams.NewRoomList(h.Storage)
	_, _, err = stream.Process(session.DeviceID, from, latestNID, "", filter)
	if err != nil {
		return &handlerError{
			err:        err,
			StatusCode: 500,
		}
	}

	// finally update our records: confirm that the client received the token they sent us, and mark this
	// response as unconfirmed
	var confirmed string
	if tokv3 != nil {
		confirmed = tokv3.String()
	}
	if err := h.Sessions.UpdateLastTokens(session.ID, confirmed, upcoming.String()); err != nil {
		return &handlerError{
			err:        err,
			StatusCode: 500,
		}
	}

	w.WriteHeader(200)
	w.Write([]byte(upcoming.String()))
	return nil
}

// getOrCreateSession retrieves an existing session if ?since= is set, else makes a new session.
// Returns a session or an error. Returns a token if and only if there is an existing session.
func (h *SyncV3Handler) getOrCreateSession(req *http.Request) (*sync3.Session, *sync3.Token, *handlerError) {
	var session *sync3.Session
	var tokv3 *sync3.Token
	deviceID, err := deviceIDFromRequest(req)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get device ID from request")
		return nil, nil, &handlerError{400, err}
	}
	sincev3 := req.URL.Query().Get("since")
	if sincev3 == "" {
		session, err = h.Sessions.NewSession(deviceID)
	} else {
		tokv3, err = sync3.NewSyncToken(sincev3)
		if err != nil {
			log.Warn().Err(err).Msg("failed to parse sync v3 token")
			return nil, nil, &handlerError{400, err}
		}
		session, err = h.Sessions.Session(tokv3.SessionID, deviceID)
	}
	if err != nil {
		log.Warn().Err(err).Str("device", deviceID).Msg("failed to ensure Session existed for device")
		return nil, nil, &handlerError{500, err}
	}
	if session.UserID == "" {
		// we need to work out the user ID to do membership queries
		userID, err := h.userIDFromRequest(req)
		if err != nil {
			log.Warn().Err(err).Msg("failed to work out user ID from request, is the authorization header valid?")
			return nil, nil, &handlerError{400, err}
		}
		session.UserID = userID
		h.Sessions.UpdateUserIDForDevice(deviceID, userID)
	}
	return session, tokv3, nil
}

// ensurePolling makes sure there is a poller for this device, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
func (h *SyncV3Handler) ensurePolling(authHeader string, session *sync3.Session) {
	h.pollerMu.Lock()
	poller, ok := h.Pollers[session.DeviceID]
	// either no poller exists or it did but it died
	if ok && !poller.Terminated {
		h.pollerMu.Unlock()
		return
	}
	// replace the poller
	poller = sync2.NewPoller(authHeader, session.DeviceID, h.V2, h.Storage, h.Sessions)
	var wg sync.WaitGroup
	wg.Add(1)
	go poller.Poll(session.V2Since, func() {
		wg.Done()
	})
	h.Pollers[session.DeviceID] = poller
	h.pollerMu.Unlock()
	wg.Wait()
}

func (h *SyncV3Handler) userIDFromRequest(req *http.Request) (string, error) {
	return h.V2.WhoAmI(req.Header.Get("Authorization"))
}

type jsonError struct {
	Err string `json:"error"`
}

func asJSONError(err error) []byte {
	je := jsonError{err.Error()}
	b, _ := json.Marshal(je)
	return b
}

func deviceIDFromRequest(req *http.Request) (string, error) {
	// return a hash of the access token
	ah := req.Header.Get("Authorization")
	if ah == "" {
		return "", fmt.Errorf("missing Authorization header")
	}
	accessToken := strings.TrimPrefix(ah, "Bearer ")
	hash := sha256.New()
	hash.Write([]byte(accessToken))
	return hex.EncodeToString(hash.Sum(nil)), nil
}
