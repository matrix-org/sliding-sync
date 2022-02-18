package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"

	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/sync3/extensions"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"github.com/rs/zerolog/log"
)

const DefaultSessionID = "default"

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

// This is a net.http Handler for sync v3. It is responsible for pairing requests to Conns and to
// ensure that the sync v2 poller is running for this client.
type SyncLiveHandler struct {
	V2         sync2.Client
	Storage    *state.Storage
	V2Store    *sync2.Storage
	PollerMap  *sync2.PollerMap
	ConnMap    *sync3.ConnMap
	Extensions *extensions.Handler

	// inserts are done by v2 poll loops, selects are done by v3 request threads
	// but the v3 requests touch non-overlapping keys, which is a good use case for sync.Map
	// > (2) when multiple goroutines read, write, and overwrite entries for disjoint sets of keys.
	userCaches *sync.Map // map[user_id]*UserCache
	Dispatcher *sync3.Dispatcher

	GlobalCache *sync3.GlobalCache
}

func NewSync3Handler(v2Client sync2.Client, postgresDBURI string) (*SyncLiveHandler, error) {
	store := state.NewStorage(postgresDBURI)
	sh := &SyncLiveHandler{
		V2:          v2Client,
		Storage:     store,
		V2Store:     sync2.NewStore(postgresDBURI),
		ConnMap:     sync3.NewConnMap(),
		userCaches:  &sync.Map{},
		Dispatcher:  sync3.NewDispatcher(),
		GlobalCache: sync3.NewGlobalCache(store),
	}
	sh.PollerMap = sync2.NewPollerMap(v2Client, sh)
	sh.Extensions = &extensions.Handler{
		Store:       store,
		E2EEFetcher: sh.PollerMap,
	}
	roomToJoinedUsers, err := store.AllJoinedMembers()
	if err != nil {
		return nil, err
	}

	if err := sh.Dispatcher.Startup(roomToJoinedUsers); err != nil {
		return nil, fmt.Errorf("failed to load sync3.Dispatcher: %s", err)
	}
	sh.Dispatcher.Register(sync3.DispatcherAllUsers, sh.GlobalCache)

	// every room will be present here
	roomIDToMetadata, err := store.MetadataForAllRooms()
	if err != nil {
		return nil, fmt.Errorf("could not get metadata for all rooms: %s", err)
	}
	if err := sh.GlobalCache.Startup(roomIDToMetadata); err != nil {
		return nil, fmt.Errorf("failed to populate global cache: %s", err)
	}

	return sh, nil
}

func (h *SyncLiveHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	err := h.serve(w, req)
	if err != nil {
		herr, ok := err.(*internal.HandlerError)
		if !ok {
			herr = &internal.HandlerError{
				StatusCode: 500,
				Err:        err,
			}
		}
		w.WriteHeader(herr.StatusCode)
		w.Write(herr.JSON())
	}
}

// Entry point for sync v3
func (h *SyncLiveHandler) serve(w http.ResponseWriter, req *http.Request) error {
	var requestBody sync3.Request
	if req.Body != nil {
		defer req.Body.Close()
		if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
			log.Err(err).Msg("failed to read/decode request body")
			return &internal.HandlerError{
				StatusCode: 400,
				Err:        err,
			}
		}
	}
	fmt.Println("incoming sync pos=", req.URL.Query().Get("pos"))
	conn, err := h.setupConnection(req, &requestBody, req.URL.Query().Get("pos") != "")
	if err != nil {
		hlog.FromRequest(req).Err(err).Msg("failed to get or create Conn")
		return err
	}
	log := hlog.FromRequest(req).With().Str("conn_id", conn.ConnID.String()).Logger()
	// set pos and timeout if specified
	cpos, herr := parseIntFromQuery(req.URL, "pos")
	if herr != nil {
		return herr
	}
	requestBody.SetPos(cpos)

	var timeout int
	if req.URL.Query().Get("timeout") == "" {
		timeout = sync3.DefaultTimeoutSecs
	} else {
		timeout64, herr := parseIntFromQuery(req.URL, "timeout")
		if herr != nil {
			return herr
		}
		timeout = int(timeout64)
	}

	requestBody.SetTimeoutSecs(timeout)

	resp, herr := conn.OnIncomingRequest(req.Context(), &requestBody)
	if herr != nil {
		log.Err(herr).Msg("failed to OnIncomingRequest")
		return herr
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		return &internal.HandlerError{
			StatusCode: 500,
			Err:        err,
		}
	}
	return nil
}

// setupConnection associates this request with an existing connection or makes a new connection.
// It also sets a v2 sync poll loop going if one didn't exist already for this user.
// When this function returns, the connection is alive and active.
func (h *SyncLiveHandler) setupConnection(req *http.Request, syncReq *sync3.Request, containsPos bool) (*sync3.Conn, error) {
	log := hlog.FromRequest(req)
	var conn *sync3.Conn

	// Identify the device
	deviceID, err := internal.DeviceIDFromRequest(req)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get device ID from request")
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        err,
		}
	}

	// client thinks they have a connection
	if containsPos {
		// Lookup the connection
		conn = h.ConnMap.Conn(sync3.ConnID{
			DeviceID: deviceID,
		})
		if err != nil {
			log.Warn().Err(err).Msg("failed to lookup conn for request")
			return nil, &internal.HandlerError{
				StatusCode: 400,
				Err:        err,
			}
		}
		if conn != nil {
			fmt.Println("returning existing conn", conn.ConnID.String())
			return conn, nil
		}
		// conn doesn't exist, we probably nuked it.
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        fmt.Errorf("session expired"),
		}
	}

	// We're going to make a new connection
	// Ensure we have the v2 side of things hooked up
	v2device, err := h.V2Store.InsertDevice(deviceID)
	if err != nil {
		log.Warn().Err(err).Str("device_id", deviceID).Msg("failed to insert v2 device")
		return nil, &internal.HandlerError{
			StatusCode: 500,
			Err:        err,
		}
	}
	if v2device.UserID == "" {
		v2device.UserID, err = h.V2.WhoAmI(req.Header.Get("Authorization"))
		if err != nil {
			log.Warn().Err(err).Str("device_id", deviceID).Msg("failed to get user ID from device ID")
			return nil, &internal.HandlerError{
				StatusCode: http.StatusBadGateway,
				Err:        err,
			}
		}
		if err = h.V2Store.UpdateUserIDForDevice(deviceID, v2device.UserID); err != nil {
			log.Warn().Err(err).Str("device_id", deviceID).Msg("failed to persist user ID -> device ID mapping")
			// non-fatal, we can still work without doing this
		}
	}
	h.PollerMap.EnsurePolling(
		req.Header.Get("Authorization"), v2device.UserID, v2device.DeviceID, v2device.Since,
		hlog.FromRequest(req).With().Str("user_id", v2device.UserID).Logger(),
	)

	userCache, err := h.userCache(v2device.UserID)
	if err != nil {
		log.Warn().Err(err).Str("user_id", v2device.UserID).Msg("failed to load user cache")
		return nil, &internal.HandlerError{
			StatusCode: 500,
			Err:        err,
		}
	}

	// Now the v2 side of things are running, we can make a v3 live sync conn
	// NB: this isn't inherently racey (we did the check for an existing conn before EnsurePolling)
	// because we *either* do the existing check *or* make a new conn. It's important for CreateConn
	// to check for an existing connection though, as it's possible for the client to call /sync
	// twice for a new connection.
	conn, created := h.ConnMap.CreateConn(sync3.ConnID{
		DeviceID: deviceID,
	}, func() sync3.ConnHandler {
		return NewConnState(v2device.UserID, v2device.DeviceID, userCache, h.GlobalCache, h.Extensions, h.Dispatcher)
	})
	if created {
		log.Info().Str("conn_id", conn.ConnID.String()).Msg("created new connection")
	} else {
		log.Info().Str("conn_id", conn.ConnID.String()).Msg("using existing connection")
	}
	return conn, nil
}

func (h *SyncLiveHandler) userCache(userID string) (*sync3.UserCache, error) {
	c, ok := h.userCaches.Load(userID)
	if ok {
		return c.(*sync3.UserCache), nil
	}
	uc := sync3.NewUserCache(userID, h.GlobalCache, h.Storage)
	// select all non-zero highlight or notif counts and set them, as this is less costly than looping every room/user pair
	err := h.Storage.UnreadTable.SelectAllNonZeroCountsForUser(userID, func(roomID string, highlightCount, notificationCount int) {
		uc.OnUnreadCounts(roomID, &highlightCount, &notificationCount)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load unread counts: %s", err)
	}
	// select the DM account data event and set DM room status
	directEvent, err := h.Storage.AccountData(userID, sync2.AccountDataGlobalRoom, "m.direct")
	if err != nil {
		return nil, fmt.Errorf("failed to load direct message status for rooms: %s", err)
	}
	if directEvent != nil {
		uc.OnAccountData([]state.AccountData{*directEvent})
	}
	h.Dispatcher.Register(userID, uc)
	h.userCaches.Store(userID, uc)
	return uc, nil
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) UpdateDeviceSince(deviceID, since string) {
	err := h.V2Store.UpdateDeviceSince(deviceID, since)
	if err != nil {
		logger.Err(err).Str("device", deviceID).Str("since", since).Msg("V2: failed to persist since token")
	}
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) Accumulate(roomID string, timeline []json.RawMessage) {
	numNew, latestPos, err := h.Storage.Accumulate(roomID, timeline)
	if err != nil {
		logger.Err(err).Int("timeline", len(timeline)).Str("room", roomID).Msg("V2: failed to accumulate room")
		return
	}
	if numNew == 0 {
		// no new events
		return
	}
	newEvents := timeline[len(timeline)-numNew:]

	// we have new events, notify active connections
	h.Dispatcher.OnNewEvents(roomID, newEvents, latestPos)
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) Initialise(roomID string, state []json.RawMessage) {
	added, err := h.Storage.Initialise(roomID, state)
	if err != nil {
		logger.Err(err).Int("state", len(state)).Str("room", roomID).Msg("V2: failed to initialise room")
		return
	}
	if !added {
		// no new events
		return
	}
	// we have new events, notify active connections
	h.Dispatcher.OnNewEvents(roomID, state, 0)
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) SetTyping(roomID string, userIDs []string) {
	_, err := h.Storage.TypingTable.SetTyping(roomID, userIDs)
	if err != nil {
		logger.Err(err).Strs("users", userIDs).Str("room", roomID).Msg("V2: failed to store typing")
	}
}

// Called from the v2 poller, implements V2DataReceiver
// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *SyncLiveHandler) AddToDeviceMessages(userID, deviceID string, msgs []json.RawMessage) {
	_, err := h.Storage.ToDeviceTable.InsertMessages(deviceID, msgs)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("device", deviceID).Int("msgs", len(msgs)).Msg("V2: failed to store to-device messages")
	}
}

func (h *SyncLiveHandler) UpdateUnreadCounts(roomID, userID string, highlightCount, notifCount *int) {
	err := h.Storage.UnreadTable.UpdateUnreadCounters(userID, roomID, highlightCount, notifCount)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to update unread counters")
	}
	userCache, ok := h.userCaches.Load(userID)
	if !ok {
		return
	}
	userCache.(*sync3.UserCache).OnUnreadCounts(roomID, highlightCount, notifCount)
}

func (h *SyncLiveHandler) OnAccountData(userID, roomID string, events []json.RawMessage) {
	data, err := h.Storage.InsertAccountData(userID, roomID, events)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to update account data")
		return
	}
	userCache, ok := h.userCaches.Load(userID)
	if !ok {
		return
	}
	userCache.(*sync3.UserCache).OnAccountData(data)
}

func parseIntFromQuery(u *url.URL, param string) (result int64, err *internal.HandlerError) {
	queryPos := u.Query().Get(param)
	if queryPos != "" {
		var err error
		result, err = strconv.ParseInt(queryPos, 10, 64)
		if err != nil {
			return 0, &internal.HandlerError{
				StatusCode: 400,
				Err:        fmt.Errorf("invalid %s: %s", param, queryPos),
			}
		}
	}
	return
}
