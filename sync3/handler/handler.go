package handler

import "C"
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/pubsub"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/prometheus/client_golang/prometheus"
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
	V2           sync2.Client
	Storage      *state.Storage
	V2Store      *sync2.Storage
	V2Sub        *pubsub.V2Sub
	EnsurePoller *EnsurePoller
	ConnMap      *sync3.ConnMap
	Extensions   *extensions.Handler

	// inserts are done by v2 poll loops, selects are done by v3 request threads
	// but the v3 requests touch non-overlapping keys, which is a good use case for sync.Map
	// > (2) when multiple goroutines read, write, and overwrite entries for disjoint sets of keys.
	userCaches *sync.Map // map[user_id]*UserCache
	Dispatcher *sync3.Dispatcher

	GlobalCache            *caches.GlobalCache
	maxPendingEventUpdates int

	numConns prometheus.Gauge
	histVec  *prometheus.HistogramVec
}

func NewSync3Handler(
	store *state.Storage, storev2 *sync2.Storage, v2Client sync2.Client, secret string,
	pub pubsub.Notifier, sub pubsub.Listener, enablePrometheus bool, maxPendingEventUpdates int,
) (*SyncLiveHandler, error) {
	logger.Info().Msg("creating handler")
	sh := &SyncLiveHandler{
		V2:                     v2Client,
		Storage:                store,
		V2Store:                storev2,
		ConnMap:                sync3.NewConnMap(),
		userCaches:             &sync.Map{},
		Dispatcher:             sync3.NewDispatcher(),
		GlobalCache:            caches.NewGlobalCache(store),
		maxPendingEventUpdates: maxPendingEventUpdates,
	}
	sh.Extensions = &extensions.Handler{
		Store:       store,
		E2EEFetcher: sh,
		GlobalCache: sh.GlobalCache,
	}

	if enablePrometheus {
		sh.addPrometheusMetrics()
		pub = pubsub.NewPromNotifier(pub, "api")
	}

	// set up pubsub mechanism to start from this point
	sh.EnsurePoller = NewEnsurePoller(pub)
	sh.V2Sub = pubsub.NewV2Sub(sub, sh)

	return sh, nil
}

func (h *SyncLiveHandler) Startup(storeSnapshot *state.StartupSnapshot) error {
	if err := h.Dispatcher.Startup(storeSnapshot.AllJoinedMembers); err != nil {
		return fmt.Errorf("failed to load sync3.Dispatcher: %s", err)
	}
	h.Dispatcher.Register(context.Background(), sync3.DispatcherAllUsers, h.GlobalCache)
	if err := h.GlobalCache.Startup(storeSnapshot.GlobalMetadata); err != nil {
		return fmt.Errorf("failed to populate global cache: %s", err)
	}
	return nil
}

// Listen starts all consumers
func (h *SyncLiveHandler) Listen() {
	go func() {
		defer internal.ReportPanicsToSentry()
		err := h.V2Sub.Listen()
		if err != nil {
			logger.Err(err).Msg("Failed to listen for v2 messages")
			sentry.CaptureException(err)
		}
	}()
}

// used in tests to close postgres connections
func (h *SyncLiveHandler) Teardown() {
	// tear down DB conns
	h.Storage.Teardown()
	h.V2Sub.Teardown()
	h.EnsurePoller.Teardown()
	h.ConnMap.Teardown()
	if h.numConns != nil {
		prometheus.Unregister(h.numConns)
	}
	if h.histVec != nil {
		prometheus.Unregister(h.histVec)
	}
}

func (h *SyncLiveHandler) updateMetrics() {
	if h.numConns == nil {
		return
	}
	h.numConns.Set(float64(h.ConnMap.Len()))
}

func (h *SyncLiveHandler) addPrometheusMetrics() {
	h.numConns = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "sliding_sync",
		Subsystem: "api",
		Name:      "num_active_conns",
		Help:      "Number of active sliding sync connections.",
	})
	h.histVec = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "sliding_sync",
		Subsystem: "api",
		Name:      "process_duration_secs",
		Help:      "Time taken in seconds for the sliding sync response to calculated, excludes long polling",
		Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"initial"})
	prometheus.MustRegister(h.numConns)
	prometheus.MustRegister(h.histVec)
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
		// artificially wait a bit before sending back the error
		// this guards against tightlooping when the client hammers the server with invalid requests
		time.Sleep(time.Second)
		w.WriteHeader(herr.StatusCode)
		w.Write(herr.JSON())
	}
}

// Entry point for sync v3
func (h *SyncLiveHandler) serve(w http.ResponseWriter, req *http.Request) error {
	var requestBody sync3.Request
	if req.ContentLength != 0 {
		defer req.Body.Close()
		if err := json.NewDecoder(req.Body).Decode(&requestBody); err != nil {
			log.Warn().Err(err).Msg("failed to read/decode request body")
			return &internal.HandlerError{
				StatusCode: 400,
				Err:        err,
			}
		}
		if err := requestBody.Validate(); err != nil {
			return &internal.HandlerError{
				StatusCode: 400,
				Err:        err,
			}
		}
	}
	hlog.FromRequest(req).UpdateContext(func(c zerolog.Context) zerolog.Context {
		c.Str("txn_id", requestBody.TxnID)
		return c
	})
	for listKey, l := range requestBody.Lists {
		if l.Ranges != nil && !l.Ranges.Valid() {
			return &internal.HandlerError{
				StatusCode: 400,
				Err:        fmt.Errorf("list[%v] invalid ranges %v", listKey, l.Ranges),
			}
		}
	}

	logErrorOrWarning := func(msg string, herr *internal.HandlerError) {
		if herr.StatusCode >= 500 {
			hlog.FromRequest(req).Err(herr).Msg(msg)
		} else {
			hlog.FromRequest(req).Warn().Err(herr).Msg(msg)
		}
	}

	conn, herr := h.setupConnection(req, &requestBody, req.URL.Query().Get("pos") != "")
	if herr != nil {
		logErrorOrWarning("failed to get or create Conn", herr)
		return herr
	}
	// set pos and timeout if specified
	cpos, herr := parseIntFromQuery(req.URL, "pos")
	if herr != nil {
		return herr
	}
	requestBody.SetPos(cpos)
	internal.SetRequestContextUserID(req.Context(), conn.UserID)
	log := hlog.FromRequest(req).With().Str("user", conn.UserID).Int64("pos", cpos).Logger()

	var timeout int
	if req.URL.Query().Get("timeout") == "" {
		timeout = sync3.DefaultTimeoutMSecs
	} else {
		timeout64, herr := parseIntFromQuery(req.URL, "timeout")
		if herr != nil {
			return herr
		}
		timeout = int(timeout64)
	}

	requestBody.SetTimeoutMSecs(timeout)
	log.Trace().Int("timeout", timeout).Msg("recv")

	resp, herr := conn.OnIncomingRequest(req.Context(), &requestBody)
	if herr != nil {
		logErrorOrWarning("failed to OnIncomingRequest", herr)
		return herr
	}
	// for logging
	var numToDeviceEvents int
	if resp.Extensions.ToDevice != nil {
		numToDeviceEvents = len(resp.Extensions.ToDevice.Events)
	}
	var numGlobalAccountData int
	if resp.Extensions.AccountData != nil {
		numGlobalAccountData = len(resp.Extensions.AccountData.Global)
	}
	var numChangedDevices, numLeftDevices int
	if resp.Extensions.E2EE != nil && resp.Extensions.E2EE.DeviceLists != nil {
		numChangedDevices = len(resp.Extensions.E2EE.DeviceLists.Changed)
		numLeftDevices = len(resp.Extensions.E2EE.DeviceLists.Left)
	}
	internal.SetRequestContextResponseInfo(
		req.Context(), cpos, resp.PosInt(), len(resp.Rooms), requestBody.TxnID, numToDeviceEvents, numGlobalAccountData,
		numChangedDevices, numLeftDevices,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		herr = &internal.HandlerError{
			StatusCode: 500,
			Err:        err,
		}
		if errors.Is(err, syscall.EPIPE) {
			// Client closed the connection. Use a 499 status code internally so that
			// we consider this a warning rather than an error. 499 is nonstandard,
			// but a) the client has already gone, so this status code will only show
			// up in our logs; and b) nginx uses 499 to mean "Client Closed Request",
			// see e.g.
			// https://www.nginx.com/resources/wiki/extending/api/http/#http-return-codes
			herr.StatusCode = 499
		}

		logErrorOrWarning("failed to JSON-encode result", herr)
		return herr
	}
	return nil
}

// setupConnection associates this request with an existing connection or makes a new connection.
// It also sets a v2 sync poll loop going if one didn't exist already for this user.
// When this function returns, the connection is alive and active.

func (h *SyncLiveHandler) setupConnection(req *http.Request, syncReq *sync3.Request, containsPos bool) (*sync3.Conn, *internal.HandlerError) {
	var conn *sync3.Conn
	// Extract an access token
	accessToken, err := internal.ExtractAccessToken(req)
	if err != nil || accessToken == "" {
		hlog.FromRequest(req).Warn().Err(err).Msg("failed to get access token from request")
		return nil, &internal.HandlerError{
			StatusCode: http.StatusUnauthorized,
			Err:        err,
		}
	}

	// Try to lookup a record of this token
	var token *sync2.Token
	token, err = h.V2Store.TokensTable.Token(accessToken)
	if err != nil {
		if err == sql.ErrNoRows {
			hlog.FromRequest(req).Info().Msg("Received connection from unknown access token, querying with homeserver")
			newToken, herr := h.identifyUnknownAccessToken(accessToken, hlog.FromRequest(req))
			if herr != nil {
				return nil, herr
			}
			token = newToken
		} else {
			hlog.FromRequest(req).Err(err).Msg("Failed to lookup access token")
			return nil, &internal.HandlerError{
				StatusCode: http.StatusInternalServerError,
				Err:        err,
			}
		}
	}
	log := hlog.FromRequest(req).With().Str("user", token.UserID).Str("device", token.DeviceID).Logger()

	// Record the fact that we've recieved a request from this token
	err = h.V2Store.TokensTable.MaybeUpdateLastSeen(token, time.Now())
	if err != nil {
		// Not fatal---log and continue.
		log.Warn().Err(err).Msg("Unable to update last seen timestamp")
	}

	connID := sync3.ConnID{
		UserID:   token.UserID,
		DeviceID: token.DeviceID,
		CID:      syncReq.ConnID,
	}
	// client thinks they have a connection
	if containsPos {
		// Lookup the connection
		conn = h.ConnMap.Conn(connID)
		if conn != nil {
			log.Trace().Str("conn", conn.ConnID.String()).Msg("reusing conn")
			return conn, nil
		}
		// conn doesn't exist, we probably nuked it.
		return nil, internal.ExpiredSessionError()
	}

	log.Trace().Msg("checking poller exists and is running")
	pid := sync2.PollerID{UserID: token.UserID, DeviceID: token.DeviceID}
	h.EnsurePoller.EnsurePolling(pid, token.AccessTokenHash)
	log.Trace().Msg("poller exists and is running")
	// this may take a while so if the client has given up (e.g timed out) by this point, just stop.
	// We'll be quicker next time as the poller will already exist.
	if req.Context().Err() != nil {
		log.Warn().Msg("client gave up, not creating connection")
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        req.Context().Err(),
		}
	}

	userCache, err := h.userCache(token.UserID)
	if err != nil {
		log.Warn().Err(err).Msg("failed to load user cache")
		return nil, &internal.HandlerError{
			StatusCode: 500,
			Err:        err,
		}
	}

	// once we have the conn, make sure our metrics are correct
	defer h.updateMetrics()

	// Now the v2 side of things are running, we can make a v3 live sync conn
	// NB: this isn't inherently racey (we did the check for an existing conn before EnsurePolling)
	// because we *either* do the existing check *or* make a new conn. It's important for CreateConn
	// to check for an existing connection though, as it's possible for the client to call /sync
	// twice for a new connection.
	conn, created := h.ConnMap.CreateConn(connID, func() sync3.ConnHandler {
		return NewConnState(token.UserID, token.DeviceID, userCache, h.GlobalCache, h.Extensions, h.Dispatcher, h.histVec, h.maxPendingEventUpdates)
	})
	if created {
		log.Info().Msg("created new connection")
	} else {
		log.Info().Msg("using existing connection")
	}
	return conn, nil
}

func (h *SyncLiveHandler) identifyUnknownAccessToken(accessToken string, logger *zerolog.Logger) (*sync2.Token, *internal.HandlerError) {
	// We don't recognise the given accessToken. Ask the homeserver who owns it.
	userID, deviceID, err := h.V2.WhoAmI(accessToken)
	if err != nil {
		if err == sync2.HTTP401 {
			return nil, &internal.HandlerError{
				StatusCode: 401,
				Err:        fmt.Errorf("/whoami returned HTTP 401"),
			}
		}
		log.Warn().Err(err).Msg("failed to get user ID from device ID")
		return nil, &internal.HandlerError{
			StatusCode: http.StatusBadGateway,
			Err:        err,
		}
	}

	var token *sync2.Token
	err = sqlutil.WithTransaction(h.V2Store.DB, func(txn *sqlx.Tx) error {
		// Create a brand-new row for this token.
		token, err = h.V2Store.TokensTable.Insert(accessToken, userID, deviceID, time.Now())
		if err != nil {
			logger.Warn().Err(err).Str("user", userID).Str("device", deviceID).Msg("failed to insert v2 token")
			return err
		}

		// Ensure we have a device row for this token.
		err = h.V2Store.DevicesTable.InsertDevice(userID, deviceID)
		if err != nil {
			log.Warn().Err(err).Str("user", userID).Str("device", deviceID).Msg("failed to insert v2 device")
			return err
		}
		return nil
	})

	if err != nil {
		return nil, &internal.HandlerError{StatusCode: 500, Err: err}
	}

	return token, nil
}

func (h *SyncLiveHandler) CacheForUser(userID string) *caches.UserCache {
	c, ok := h.userCaches.Load(userID)
	if ok {
		return c.(*caches.UserCache)
	}
	return nil
}

// userCache fetches an existing caches.UserCache for this user if one exists. If not,
// it
//   - creates a blank caches.UserCache struct,
//   - fires callbacks on that struct as necessary to populate it with initial state,
//   - stores the struct so it will not be recreated in the future, and
//   - registers the cache with the Dispatcher.
//
// Some extra initialisation takes place in caches.UserCache.OnRegister.
// TODO: the calls to uc.OnBlahBlah etc can be moved into NewUserCache, now that the
//
//	UserCache holds a reference to the storage layer.
func (h *SyncLiveHandler) userCache(userID string) (*caches.UserCache, error) {
	// bail if we already have a cache
	c, ok := h.userCaches.Load(userID)
	if ok {
		return c.(*caches.UserCache), nil
	}
	uc := caches.NewUserCache(userID, h.GlobalCache, h.Storage, h)
	// select all non-zero highlight or notif counts and set them, as this is less costly than looping every room/user pair
	err := h.Storage.UnreadTable.SelectAllNonZeroCountsForUser(userID, func(roomID string, highlightCount, notificationCount int) {
		uc.OnUnreadCounts(context.Background(), roomID, &highlightCount, &notificationCount)
	})
	if err != nil {
		return nil, fmt.Errorf("failed to load unread counts: %s", err)
	}
	// select the DM account data event and set DM room status
	directEvent, err := h.Storage.AccountData(userID, sync2.AccountDataGlobalRoom, []string{"m.direct"})
	if err != nil {
		return nil, fmt.Errorf("failed to load direct message status for rooms: %s", err)
	}
	if len(directEvent) == 1 {
		uc.OnAccountData(context.Background(), []state.AccountData{directEvent[0]})
	}

	// select all room tag account data and set it
	tagEvents, err := h.Storage.RoomAccountDatasWithType(userID, "m.tag")
	if err != nil {
		return nil, fmt.Errorf("failed to load room tags %s", err)
	}
	if len(tagEvents) > 0 {
		uc.OnAccountData(context.Background(), tagEvents)
	}

	// select outstanding invites
	invites, err := h.Storage.InvitesTable.SelectAllInvitesForUser(userID)
	if err != nil {
		return nil, fmt.Errorf("failed to load outstanding invites for user: %s", err)
	}
	for roomID, inviteState := range invites {
		uc.OnInvite(context.Background(), roomID, inviteState)
	}

	// use LoadOrStore here else we can race as 2 brand new /sync conns can both get to this point
	// at the same time
	actualUC, loaded := h.userCaches.LoadOrStore(userID, uc)
	uc = actualUC.(*caches.UserCache)
	if !loaded { // we actually inserted the cache, so register with the dispatcher.
		if err = h.Dispatcher.Register(context.Background(), userID, uc); err != nil {
			h.Dispatcher.Unregister(userID)
			h.userCaches.Delete(userID)
			return nil, fmt.Errorf("failed to register user cache with dispatcher: %s", err)
		}
	}

	return uc, nil
}

// Implements E2EEFetcher
// DeviceData returns the latest device data for this user. isInitial should be set if this is for
// an initial /sync request.
func (h *SyncLiveHandler) DeviceData(ctx context.Context, userID, deviceID string, isInitial bool) *internal.DeviceData {
	// We have 2 sources of DeviceData:
	// - pubsub updates stored in deviceDataMap
	// - the database itself
	// Most of the time we would like to pull from deviceDataMap and ignore the database entirely,
	// however in most cases we need to do a database hit to atomically swap device lists over. Why?
	//
	// changed|left are much more important and special because:
	//
	// - sync v2 only sends deltas, rather than all of them unlike otk counts and fallback key types
	// - we MUST guarantee that we send this to the client, as missing a user in `changed` can result in us having the wrong
	//   device lists for that user resulting in encryption breaking when the client encrypts for known devices.
	// - we MUST NOT continually send the same device list changes on each subsequent request i.e we need to delete them
	//
	// We accumulate device list deltas on the v2 poller side, upserting into the database and sending pubsub notifs for.
	// The accumulated deltas are stored in DeviceData.DeviceLists.New
	// To guarantee we send this to the client, we need to consider a few failure modes:
	// - The response is lost and the request is retried to this proxy -> ConnMap caches will get it.
	// - The response is lost and the client doesn't retry until the connection expires. They then retry ->
	//   ConnMap cache miss, sends HTTP 400 due to invalid ?pos=
	// - The response is received and the client sends the next request -> do not send deltas.

	// To handle the case where responses are lost, we just need to see if this is an initial request
	// and if so, return a "Read-Only" snapshot of the last sent device list changes. This means we may send
	// duplicate device list changes if the response did in fact get to the client and the next request hit a
	// new proxy, but that's better than losing updates. In this scenario, we do not delete any data.
	// To ensure we delete device list updates over time, we now want to swap what was New to Sent and then
	// send Sent. That means we forget what was originally in Sent and New is empty. We need to read and swap
	// atomically else the v2 poller may insert a new update after the read but before the swap (DELETE on New)
	// To ensure atomicity, we need to do this in a txn.
	// Atomically move New to Sent so New is now empty and what was originally in Sent is forgotten.
	shouldSwap := !isInitial

	dd, err := h.Storage.DeviceDataTable.Select(userID, deviceID, shouldSwap)
	if err != nil {
		logger.Err(err).Str("user", userID).Msg("failed to SelectAndSwap device data")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return nil
	}

	return dd
}

// Implements TransactionIDFetcher
func (h *SyncLiveHandler) TransactionIDForEvents(userID string, deviceID string, eventIDs []string) (eventIDToTxnID map[string]string) {
	eventIDToTxnID, err := h.Storage.TransactionsTable.Select(userID, deviceID, eventIDs)
	if err != nil {
		logger.Warn().Str("err", err.Error()).Str("device", deviceID).Msg("failed to select txn IDs for events")
	}
	return
}

func (h *SyncLiveHandler) OnInitialSyncComplete(p *pubsub.V2InitialSyncComplete) {
	h.EnsurePoller.OnInitialSyncComplete(p)
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) Accumulate(p *pubsub.V2Accumulate) {
	ctx, task := internal.StartTask(context.Background(), "Accumulate")
	defer task.End()
	// note: events is sorted in ascending NID order, event if p.EventNIDs isn't.
	events, err := h.Storage.EventNIDs(p.EventNIDs)
	if err != nil {
		logger.Err(err).Str("room", p.RoomID).Msg("Accumulate: failed to EventNIDs")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}
	if len(events) == 0 {
		return
	}
	internal.Logf(ctx, "room", fmt.Sprintf("%s: %d events", p.RoomID, len(events)))
	// we have new events, notify active connections
	for i := range events {
		h.Dispatcher.OnNewEvent(ctx, p.RoomID, events[i], p.EventNIDs[i])
	}
}

// OnTransactionID is called from the v2 poller, implements V2DataReceiver.
func (h *SyncLiveHandler) OnTransactionID(p *pubsub.V2TransactionID) {
	_, task := internal.StartTask(context.Background(), "TransactionID")
	defer task.End()
	// TODO implement me
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) Initialise(p *pubsub.V2Initialise) {
	ctx, task := internal.StartTask(context.Background(), "Initialise")
	defer task.End()
	state, err := h.Storage.StateSnapshot(p.SnapshotNID)
	if err != nil {
		logger.Err(err).Int64("snap", p.SnapshotNID).Str("room", p.RoomID).Msg("Initialise: failed to get StateSnapshot")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}
	// we have new state, notify caches
	h.Dispatcher.OnNewInitialRoomState(ctx, p.RoomID, state)
}

func (h *SyncLiveHandler) OnUnreadCounts(p *pubsub.V2UnreadCounts) {
	ctx, task := internal.StartTask(context.Background(), "OnUnreadCounts")
	defer task.End()
	userCache, ok := h.userCaches.Load(p.UserID)
	if !ok {
		return
	}
	userCache.(*caches.UserCache).OnUnreadCounts(ctx, p.RoomID, p.HighlightCount, p.NotificationCount)
}

// push device data updates on waiting conns (otk counts, device list changes)
func (h *SyncLiveHandler) OnDeviceData(p *pubsub.V2DeviceData) {
	ctx, task := internal.StartTask(context.Background(), "OnDeviceData")
	defer task.End()
	conns := h.ConnMap.Conns(p.UserID, p.DeviceID)
	for _, conn := range conns {
		conn.OnUpdate(ctx, caches.DeviceDataUpdate{})
	}
}

func (h *SyncLiveHandler) OnDeviceMessages(p *pubsub.V2DeviceMessages) {
	ctx, task := internal.StartTask(context.Background(), "OnDeviceMessages")
	defer task.End()
	conns := h.ConnMap.Conns(p.UserID, p.DeviceID)
	for _, conn := range conns {
		conn.OnUpdate(ctx, caches.DeviceEventsUpdate{})
	}

}

func (h *SyncLiveHandler) OnInvite(p *pubsub.V2InviteRoom) {
	ctx, task := internal.StartTask(context.Background(), "OnInvite")
	defer task.End()
	userCache, ok := h.userCaches.Load(p.UserID)
	if !ok {
		return
	}
	inviteState, err := h.Storage.InvitesTable.SelectInviteState(p.UserID, p.RoomID)
	if err != nil {
		logger.Err(err).Str("user", p.UserID).Str("room", p.RoomID).Msg("failed to get invite state")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}
	userCache.(*caches.UserCache).OnInvite(ctx, p.RoomID, inviteState)
}

func (h *SyncLiveHandler) OnLeftRoom(p *pubsub.V2LeaveRoom) {
	ctx, task := internal.StartTask(context.Background(), "OnLeftRoom")
	defer task.End()
	userCache, ok := h.userCaches.Load(p.UserID)
	if !ok {
		return
	}
	userCache.(*caches.UserCache).OnLeftRoom(ctx, p.RoomID)
}

func (h *SyncLiveHandler) OnReceipt(p *pubsub.V2Receipt) {
	ctx, task := internal.StartTask(context.Background(), "OnReceipt")
	defer task.End()
	// split receipts into public / private
	userToPrivateReceipts := make(map[string][]internal.Receipt)
	publicReceipts := make([]internal.Receipt, 0, len(p.Receipts))
	for _, r := range p.Receipts {
		if r.IsPrivate {
			userToPrivateReceipts[r.UserID] = append(userToPrivateReceipts[r.UserID], r)
		} else {
			publicReceipts = append(publicReceipts, r)
		}
	}
	// always send private receipts, directly to the connected user cache if one exists
	for userID, privateReceipts := range userToPrivateReceipts {
		userCache, ok := h.userCaches.Load(userID)
		if !ok {
			continue
		}
		for _, pr := range privateReceipts {
			userCache.(*caches.UserCache).OnReceipt(ctx, pr)
		}
	}
	if len(publicReceipts) == 0 {
		return
	}
	// inform the dispatcher of global receipts
	for _, pr := range publicReceipts {
		h.Dispatcher.OnReceipt(ctx, pr)
	}
}

func (h *SyncLiveHandler) OnTyping(p *pubsub.V2Typing) {
	ctx, task := internal.StartTask(context.Background(), "OnTyping")
	defer task.End()
	rooms := h.GlobalCache.LoadRooms(ctx, p.RoomID)
	if rooms[p.RoomID] != nil {
		if reflect.DeepEqual(p.EphemeralEvent, rooms[p.RoomID].TypingEvent) {
			return // it's a duplicate, which happens when 2+ users are in the same room
		}
	}
	h.Dispatcher.OnEphemeralEvent(ctx, p.RoomID, p.EphemeralEvent)
}

func (h *SyncLiveHandler) OnAccountData(p *pubsub.V2AccountData) {
	ctx, task := internal.StartTask(context.Background(), "OnAccountData")
	defer task.End()
	userCache, ok := h.userCaches.Load(p.UserID)
	if !ok {
		return
	}
	data, err := h.Storage.AccountData(p.UserID, p.RoomID, p.Types)
	if err != nil {
		logger.Err(err).Str("user", p.UserID).Str("room", p.RoomID).Msg("OnAccountData: failed to lookup")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}
	userCache.(*caches.UserCache).OnAccountData(ctx, data)
}

func (h *SyncLiveHandler) OnExpiredToken(p *pubsub.V2ExpiredToken) {
	h.EnsurePoller.OnExpiredToken(p)
	h.ConnMap.CloseConnsForDevice(p.UserID, p.DeviceID)
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
