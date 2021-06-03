package syncv3

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/justinas/alice"
	"github.com/matrix-org/sync-v3/state"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
)

var log = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

// RunSyncV3Server is the main entry point to the server
func RunSyncV3Server(destinationServer, bindAddr, postgresDBURI string) {
	// setup logging
	c := alice.New()
	c = c.Append(hlog.NewHandler(log))
	c = c.Append(hlog.AccessHandler(func(r *http.Request, status, size int, duration time.Duration) {
		hlog.FromRequest(r).Info().
			Str("method", r.Method).
			Int("status", status).
			Int("size", size).
			Dur("duration", duration).
			Str("since", r.URL.Query().Get("since")).
			Msg("")
	}))
	c = c.Append(hlog.RemoteAddrHandler("ip"))

	// dependency inject all components together
	sh := &SyncV3Handler{
		V2: &V2{
			Client: &http.Client{
				Timeout: 120 * time.Second,
			},
			DestinationServer: destinationServer,
		},
		Sessions:    NewSessions(postgresDBURI),
		Accumulator: state.NewAccumulator(postgresDBURI),
	}

	// HTTP path routing
	r := mux.NewRouter()
	r.Handle("/_matrix/client/v3/sync", sh)
	handler := c.Then(r)

	// Block forever
	log.Info().Msgf("listening on %s", bindAddr)
	if err := http.ListenAndServe(bindAddr, handler); err != nil {
		log.Fatal().Err(err).Msg("failed to listen and serve")
	}
}

type SyncV3Handler struct {
	V2          *V2
	Sessions    *Sessions
	Accumulator *state.Accumulator
}

func (h *SyncV3Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	deviceID, err := deviceIDFromRequest(req)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get device ID from request")
		w.WriteHeader(400)
		w.Write(asJSONError(err))
		return
	}
	// Get or create a Session
	var session *Session
	var tokv3 *v3token
	sincev3 := req.URL.Query().Get("since")
	if sincev3 == "" {
		session, err = h.Sessions.NewSession(deviceID)
	} else {
		tokv3, err = newSyncV3Token(sincev3)
		if err != nil {
			log.Warn().Err(err).Msg("failed to parse sync v3 token")
			w.WriteHeader(400)
			w.Write(asJSONError(err))
			return
		}
		session, err = h.Sessions.Session(tokv3.sessionID, deviceID)
	}
	if err != nil {
		log.Warn().Err(err).Str("device", deviceID).Msg("failed to ensure Session existed for device")
		w.WriteHeader(500)
		w.Write(asJSONError(err))
		return
	}
	log.Info().Str("session", session.ID).Str("device", session.DeviceID).Msg("recv /v3/sync")

	// map sync v3 token to sync v2 token
	var sincev2 string
	if tokv3 != nil {
		sincev2 = tokv3.v2token
	}

	// query for the data
	v2res, err := h.V2.DoSyncV2(req.Header.Get("Authorization"), sincev2)
	if err != nil {
		log.Warn().Err(err).Msg("DoSyncV2 failed")
		w.WriteHeader(502)
		w.Write(asJSONError(err))
		return
	}

	h.accumulate(v2res)

	// return data based on filters

	v3res, err := json.Marshal(v2res)
	if err != nil {
		w.WriteHeader(500)
		w.Write(asJSONError(err))
		return
	}
	w.Header().Set("X-Matrix-Sync-V3", v3token{
		v2token:   v2res.NextBatch,
		sessionID: session.ID,
		filterIDs: []string{},
	}.String())
	w.WriteHeader(200)
	w.Write(v3res)
}

func (h *SyncV3Handler) accumulate(res *SyncV2Response) {
	for roomID, roomData := range res.Rooms.Join {
		if len(roomData.State.Events) > 0 {
			err := h.Accumulator.Initialise(roomID, roomData.State.Events)
			if err != nil {
				log.Err(err).Str("room_id", roomID).Int("num_state_events", len(roomData.State.Events)).Msg("Accumulator.Initialise failed")
			}
		}
		err := h.Accumulator.Accumulate(roomID, roomData.Timeline.Events)
		if err != nil {
			log.Err(err).Str("room_id", roomID).Int("num_timeline_events", len(roomData.Timeline.Events)).Msg("Accumulator.Accumulate failed")
		}
	}
	log.Info().Int("num_rooms", len(res.Rooms.Join)).Msg("accumulated data")
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
