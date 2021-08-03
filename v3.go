package syncv3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/notifier"
	"github.com/matrix-org/sync-v3/sync3/store"
	"github.com/matrix-org/sync-v3/sync3/streams"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"github.com/tidwall/gjson"
)

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
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	})
	// dependency inject all components together
	sh := NewSyncV3Handler(&sync2.HTTPClient{
		Client: &http.Client{
			Timeout: 5 * time.Minute,
		},
		DestinationServer: destinationServer,
	}, postgresDBURI)

	// HTTP path routing
	r := mux.NewRouter()
	r.Handle("/_matrix/client/v3/sync", sh)

	srv := &server{
		chain: []func(next http.Handler) http.Handler{
			hlog.NewHandler(logger),
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
	logger.Info().Msgf("listening on %s", bindAddr)
	if err := http.ListenAndServe(bindAddr, srv); err != nil {
		logger.Fatal().Err(err).Msg("failed to listen and serve")
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
	V2       sync2.Client
	Sessions *store.Sessions
	Storage  *state.Storage
	Notifier *notifier.Notifier

	typingStream *streams.Typing

	pollerMu *sync.Mutex
	Pollers  map[string]*sync2.Poller // device_id -> poller
}

func NewSyncV3Handler(v2Client sync2.Client, postgresDBURI string) *SyncV3Handler {
	sh := &SyncV3Handler{
		V2:       v2Client,
		Sessions: store.NewSessions(postgresDBURI),
		Storage:  state.NewStorage(postgresDBURI),
		Pollers:  make(map[string]*sync2.Poller),
		pollerMu: &sync.Mutex{},
	}
	sh.typingStream = streams.NewTyping(sh.Storage)
	latestToken := sync3.Token{}
	nid, err := sh.Storage.LatestEventNID()
	if err != nil {
		panic(err)
	}
	latestToken.NID = nid
	typingID, err := sh.Storage.LatestTypingID()
	if err != nil {
		panic(err)
	}
	latestToken.TypingPosition = typingID
	sh.Notifier = notifier.NewNotifier(latestToken)
	// TODO: load up the membership states so the notifier knows who to wake up
	/*
		roomIDToUserIDs, err := sh.Storage.AllJoinedMembers()
		if err != nil {
			panic(err)
		}
		sh.Notifier.Load(roomIDToUserIDs) */
	return sh
}

func (h *SyncV3Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	err := h.serve(w, req)
	if err != nil {
		w.WriteHeader(err.StatusCode)
		w.Write(asJSONError(err))
	}
}

func (h *SyncV3Handler) serve(w http.ResponseWriter, req *http.Request) *handlerError {
	session, fromToken, herr := h.getOrCreateSession(req)
	if herr != nil {
		return herr
	}
	log := hlog.FromRequest(req).With().Int64("session", session.ID).Logger()
	log.Info().Str("device", session.DeviceID).Str("user_id", session.UserID).Str("since", req.URL.Query().Get("since")).Str("from", fromToken.String()).Msg("recv /v3/sync")

	// make sure we have a poller for this device
	h.ensurePolling(req.Header.Get("Authorization"), session, log)

	// fetch the latest value which we'll base our response on
	upcoming := h.Notifier.CurrentPosition()
	upcoming.AssociateWithUser(*fromToken)
	timeout := req.URL.Query().Get("timeout")
	if timeout == "" {
		timeout = "0"
	}
	timeoutMs, err := strconv.ParseInt(timeout, 10, 64)
	if err != nil {
		return &handlerError{
			StatusCode: 400,
			err:        fmt.Errorf("?timeout= isn't an integer"),
		}
	}

	// read filters and mux in to form complete request. We MUST do this before waiting on sync streams
	// so that we update filters even in the event that we time out
	syncReq, filterID, herr := h.parseRequest(req, fromToken, session)
	if herr != nil {
		return herr
	}
	// if there was a change to the filters, update the filter ID
	if filterID != 0 {
		upcoming.FilterID = filterID
	}

	if !shouldReturnImmediately(fromToken, &upcoming, timeoutMs) {
		// from == upcoming so we need to block for up to timeoutMs for a new event to come in
		log.Info().Int64("timeout_ms", timeoutMs).Msg("blocking")
		newUpcoming := h.waitForEvents(req.Context(), session, *fromToken, time.Duration(timeoutMs)*time.Millisecond)
		if newUpcoming == nil {
			// no data
			w.WriteHeader(200)
			var result streams.Response
			result.Next = upcoming.String()
			if err := json.NewEncoder(w).Encode(&result); err != nil {
				log.Warn().Err(err).Msg("failed to marshal response")
			}
			return nil
		}
		upcoming.ApplyUpdates(*newUpcoming)
	}

	// start making the response
	resp := streams.Response{}

	// invoke streams to get responses
	if syncReq.Typing != nil {
		typingResp, typingTo, err := h.typingStream.Process(session.UserID, fromToken.TypingPosition, syncReq.Typing)
		if err != nil {
			return &handlerError{
				StatusCode: 500,
				err:        fmt.Errorf("typing stream: %s", err),
			}
		}
		upcoming.TypingPosition = typingTo
		resp.Typing = typingResp
	}
	if syncReq.ToDevice != nil {
	}

	resp.Next = upcoming.String()

	// finally update our records: confirm that the client received the token they sent us, and mark this
	// response as unconfirmed
	confirmed := fromToken.String()
	log.Info().Str("since", confirmed).Str("new_since", upcoming.String()).Bool(
		"typing_stream", syncReq.Typing != nil,
	).Msg("responding")
	if err := h.Sessions.UpdateLastTokens(session.ID, confirmed, upcoming.String()); err != nil {
		return &handlerError{
			err:        err,
			StatusCode: 500,
		}
	}

	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(&resp); err != nil {
		log.Warn().Err(err).Msg("failed to marshal response")
	}
	return nil
}

// getOrCreateSession retrieves an existing session if ?since= is set, else makes a new session.
// Returns a session or an error.
func (h *SyncV3Handler) getOrCreateSession(req *http.Request) (*sync3.Session, *sync3.Token, *handlerError) {
	log := hlog.FromRequest(req)
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
	if session == nil {
		return nil, nil, &handlerError{400, fmt.Errorf("unknown session; since = %s session ID = %d device ID = %s", sincev3, tokv3.SessionID, deviceID)}
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
	if tokv3 == nil {
		tokv3 = &sync3.Token{
			SessionID: session.ID,
		}
	}
	return session, tokv3, nil
}

// ensurePolling makes sure there is a poller for this device, making one if need be.
// Blocks until at least 1 sync is done if and only if the poller was just created.
// This ensures that calls to the database will return data.
func (h *SyncV3Handler) ensurePolling(authHeader string, session *sync3.Session, logger zerolog.Logger) {
	h.pollerMu.Lock()
	poller, ok := h.Pollers[session.DeviceID]
	// either no poller exists or it did but it died
	if ok && !poller.Terminated {
		h.pollerMu.Unlock()
		return
	}
	// replace the poller
	poller = sync2.NewPoller(session.UserID, authHeader, session.DeviceID, h.V2, h, logger)
	var wg sync.WaitGroup
	wg.Add(1)
	go poller.Poll(session.V2Since, func() {
		wg.Done()
	})
	h.Pollers[session.DeviceID] = poller
	h.pollerMu.Unlock()
	wg.Wait()
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncV3Handler) UpdateDeviceSince(deviceID, since string) error {
	return h.Sessions.UpdateDeviceSince(deviceID, since)
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncV3Handler) Accumulate(roomID string, timeline []json.RawMessage) error {
	numNew, err := h.Storage.Accumulate(roomID, timeline)
	if err != nil {
		return err
	}
	if numNew == 0 {
		return nil
	}
	var updateToken sync3.Token
	// TODO: read from memory, persist in Storage?
	updateToken.NID, err = h.Storage.LatestEventNID()
	if err != nil {
		return err
	}
	newEvents := timeline[len(timeline)-numNew:]
	for _, eventJSON := range newEvents {
		event := gjson.ParseBytes(eventJSON)
		h.Notifier.OnNewEvent(
			roomID, event.Get("sender").Str, event.Get("type").Str,
			event.Get("state_key").Str, event.Get("content.membership").Str, nil, updateToken,
		)
	}
	return nil
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncV3Handler) Initialise(roomID string, state []json.RawMessage) error {
	added, err := h.Storage.Initialise(roomID, state)
	if err != nil {
		return err
	}
	if added {
		for _, eventJSON := range state {
			event := gjson.ParseBytes(eventJSON)
			if event.Get("type").Str == "m.room.member" {
				target := event.Get("state_key").Str
				membership := event.Get("content.membership").Str
				if membership == "join" {
					h.Notifier.AddJoinedUser(roomID, target)
				} else if membership == "ban" || membership == "leave" {
					h.Notifier.RemoveJoinedUser(roomID, target)
				}
			}
		}
	}
	return nil
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncV3Handler) SetTyping(roomID string, userIDs []string) (int64, error) {
	typingID, err := h.Storage.TypingTable.SetTyping(roomID, userIDs)
	if err != nil {
		return 0, err
	}
	var updateToken sync3.Token
	updateToken.TypingPosition = typingID
	h.Notifier.OnNewTyping(roomID, updateToken)
	return typingID, nil
}

func (h *SyncV3Handler) AddToDeviceMessages(userID, deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent) error {
	pos, err := h.Storage.ToDeviceTable.InsertMessages(deviceID, msgs)
	if err != nil {
		return err
	}
	var updateToken sync3.Token
	updateToken.ToDevicePosition = pos
	h.Notifier.OnNewSendToDevice(userID, []string{deviceID}, updateToken)
	return nil
}

func (h *SyncV3Handler) parseRequest(req *http.Request, tok *sync3.Token, session *sync3.Session) (*streams.Request, int64, *handlerError) {
	existing := &streams.Request{} // first request
	var err error
	if tok.FilterID != 0 {
		// load existing filter
		existing, err = h.Sessions.Filter(tok.SessionID, tok.FilterID)
		if err != nil {
			return nil, 0, &handlerError{
				StatusCode: 400,
				err:        fmt.Errorf("failed to load filters from sync token: %s", err),
			}
		}
	}
	// load new delta from request
	defer req.Body.Close()
	var delta streams.Request
	if err := json.NewDecoder(req.Body).Decode(&delta); err != nil {
		return nil, 0, &handlerError{
			StatusCode: 400,
			err:        fmt.Errorf("failed to parse request body as JSON: %s", err),
		}
	}
	var filterID int64
	if existing.ApplyDeltas(&delta) {
		// persist new filters if there were deltas
		filterID, err = h.Sessions.InsertFilter(session.ID, existing)
		if err != nil {
			return nil, 0, &handlerError{
				StatusCode: 500,
				err:        fmt.Errorf("failed to persist filters: %s", err),
			}
		}
	}

	return existing, filterID, nil
}

func (h *SyncV3Handler) userIDFromRequest(req *http.Request) (string, error) {
	return h.V2.WhoAmI(req.Header.Get("Authorization"))
}

func (h *SyncV3Handler) waitForEvents(ctx context.Context, session *sync3.Session, since sync3.Token, timeout time.Duration) *sync3.Token {
	listener := h.Notifier.GetListener(ctx, *session)
	defer listener.Close()
	select {
	case <-ctx.Done():
		// caller gave up
		return nil
	case <-time.After(timeout):
		// timed out
		return nil
	case <-listener.GetNotifyChannel(since):
		// new data!
		p := listener.GetSyncPosition()
		return &p
	}
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

func shouldReturnImmediately(fromToken, upcoming *sync3.Token, timeoutMs int64) bool {
	if timeoutMs == 0 {
		return true
	}
	return upcoming.IsAfter(*fromToken)
}
