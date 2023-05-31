package handler2

import (
	"encoding/json"
	"github.com/getsentry/sentry-go"
	"hash/fnv"
	"os"
	"sync"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/pubsub"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

// Handler is responsible for starting v2 pollers at startup,
// processing v2 data and publishing updates, and receiving and processing EnsurePolling events.
type Handler struct {
	pMap      *sync2.PollerMap
	v2Store   *sync2.Storage
	Store     *state.Storage
	v2Pub     pubsub.Notifier
	v3Sub     *pubsub.V3Sub
	client    sync2.Client
	unreadMap map[string]struct {
		Highlight int
		Notif     int
	}
	// room_id => fnv_hash([typing user ids])
	typingMap map[string]uint64

	numPollers prometheus.Gauge
	subSystem  string
}

func NewHandler(
	connStr string, pMap *sync2.PollerMap, v2Store *sync2.Storage, store *state.Storage, client sync2.Client,
	pub pubsub.Notifier, sub pubsub.Listener, enablePrometheus bool,
) (*Handler, error) {
	h := &Handler{
		pMap:      pMap,
		v2Store:   v2Store,
		client:    client,
		Store:     store,
		subSystem: "poller",
		unreadMap: make(map[string]struct {
			Highlight int
			Notif     int
		}),
		typingMap: make(map[string]uint64),
	}
	pMap.SetCallbacks(h)

	if enablePrometheus {
		h.addPrometheusMetrics()
		pub = pubsub.NewPromNotifier(pub, h.subSystem)
	}
	h.v2Pub = pub

	// listen for v3 requests like requests to start polling
	v3Sub := pubsub.NewV3Sub(sub, h)
	h.v3Sub = v3Sub
	return h, nil
}

// Listen starts all consumers
func (h *Handler) Listen() {
	go func() {
		defer internal.ReportPanicsToSentry()
		err := h.v3Sub.Listen()
		if err != nil {
			logger.Err(err).Msg("Failed to listen for v3 messages")
			sentry.CaptureException(err)
		}
	}()
}

func (h *Handler) Teardown() {
	// stop polling and tear down DB conns
	h.v3Sub.Teardown()
	h.v2Pub.Close()
	h.Store.Teardown()
	h.v2Store.Teardown()
	h.pMap.Terminate()
	if h.numPollers != nil {
		prometheus.Unregister(h.numPollers)
	}
}

func (h *Handler) StartV2Pollers() {
	tokens, err := h.v2Store.TokensTable.TokenForEachDevice(nil)
	if err != nil {
		logger.Err(err).Msg("StartV2Pollers: failed to query tokens")
		sentry.CaptureException(err)
		return
	}
	// how many concurrent pollers to make at startup.
	// Too high and this will flood the upstream server with sync requests at startup.
	// Too low and this will take ages for the v2 pollers to startup.
	numWorkers := 16
	numFails := 0
	ch := make(chan sync2.TokenForPoller, len(tokens))
	for _, t := range tokens {
		// if we fail to decrypt the access token, skip it.
		if t.AccessToken == "" {
			numFails++
			continue
		}
		ch <- t
	}
	close(ch)
	logger.Info().Int("num_devices", len(tokens)).Int("num_fail_decrypt", numFails).Msg("StartV2Pollers")
	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for i := 0; i < numWorkers; i++ {
		go func() {
			defer wg.Done()
			for t := range ch {
				pid := sync2.PollerID{
					UserID:   t.UserID,
					DeviceID: t.DeviceID,
				}
				h.pMap.EnsurePolling(
					pid, t.AccessToken, t.Since, true,
					logger.With().Str("user_id", t.UserID).Str("device_id", t.DeviceID).Logger(),
				)
				h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2InitialSyncComplete{
					UserID:   t.UserID,
					DeviceID: t.DeviceID,
				})
			}
		}()
	}
	wg.Wait()
	logger.Info().Msg("StartV2Pollers finished")
	h.updateMetrics()
}

func (h *Handler) updateMetrics() {
	if h.numPollers == nil {
		return
	}
	h.numPollers.Set(float64(h.pMap.NumPollers()))
}

func (h *Handler) OnTerminated(userID, deviceID string) {
	h.updateMetrics()
}

func (h *Handler) OnExpiredToken(accessTokenHash, userID, deviceID string) {
	h.v2Store.TokensTable.Delete(accessTokenHash)
	// Notify v3 side so it can remove the connection from ConnMap
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2ExpiredToken{
		UserID:   userID,
		DeviceID: deviceID,
	})
}

func (h *Handler) addPrometheusMetrics() {
	h.numPollers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "sliding_sync",
		Subsystem: h.subSystem,
		Name:      "num_pollers",
		Help:      "Number of active sync v2 pollers.",
	})
	prometheus.MustRegister(h.numPollers)
}

// Emits nothing as no downstream components need it.
func (h *Handler) UpdateDeviceSince(userID, deviceID, since string) {
	err := h.v2Store.DevicesTable.UpdateDeviceSince(userID, deviceID, since)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("device", deviceID).Str("since", since).Msg("V2: failed to persist since token")
		sentry.CaptureException(err)
	}
}

func (h *Handler) OnE2EEData(userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) {
	// some of these fields may be set
	partialDD := internal.DeviceData{
		UserID:           userID,
		DeviceID:         deviceID,
		OTKCounts:        otkCounts,
		FallbackKeyTypes: fallbackKeyTypes,
		DeviceLists: internal.DeviceLists{
			New: deviceListChanges,
		},
	}
	nextPos, err := h.Store.DeviceDataTable.Upsert(&partialDD)
	if err != nil {
		logger.Err(err).Str("user", userID).Msg("failed to upsert device data")
		sentry.CaptureException(err)
		return
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2DeviceData{
		UserID:   userID,
		DeviceID: deviceID,
		Pos:      nextPos,
	})
}

func (h *Handler) Accumulate(userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) {
	// Remember any transaction IDs that may be unique to this user
	eventIDToTxnID := make(map[string]string, len(timeline)) // event_id -> txn_id
	for _, e := range timeline {
		txnID := gjson.GetBytes(e, "unsigned.transaction_id")
		if !txnID.Exists() {
			continue
		}
		eventID := gjson.GetBytes(e, "event_id").Str
		eventIDToTxnID[eventID] = txnID.Str
	}
	if len(eventIDToTxnID) > 0 {
		// persist the txn IDs
		err := h.Store.TransactionsTable.Insert(userID, deviceID, eventIDToTxnID)
		if err != nil {
			logger.Err(err).Str("user", userID).Str("device", deviceID).Int("num_txns", len(eventIDToTxnID)).Msg("failed to persist txn IDs for user")
			sentry.CaptureException(err)
		}
	}

	// Insert new events
	numNew, latestNIDs, roomReplacements, err := h.Store.Accumulate(roomID, prevBatch, timeline)
	if err != nil {
		logger.Err(err).Int("timeline", len(timeline)).Str("room", roomID).Msg("V2: failed to accumulate room")
		sentry.CaptureException(err)
		return
	}
	if numNew == 0 {
		// no new events
		return
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2Accumulate{
		RoomID:    roomID,
		PrevBatch: prevBatch,
		EventNIDs: latestNIDs,
		// TODO: for now I've just shoved this into V2Accumulate. But maybe we ought to
		// have a new pubsub Payload type, say V2RoomReplacement dedicated to this?
		// TODO: push this through to the V2DataReceiver and update caches on the V3 side?
		RoomReplacements: roomReplacements,
	})
}

func (h *Handler) Initialise(roomID string, state []json.RawMessage) []json.RawMessage {
	res, err := h.Store.Initialise(roomID, state)
	if err != nil {
		logger.Err(err).Int("state", len(state)).Str("room", roomID).Msg("V2: failed to initialise room")
		sentry.CaptureException(err)
		return nil
	}
	if res.AddedEvents {
		h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2Initialise{
			RoomID:      roomID,
			SnapshotNID: res.SnapshotID,
		})
	}
	return res.PrependTimelineEvents
}

func (h *Handler) SetTyping(roomID string, ephEvent json.RawMessage) {
	next := typingHash(ephEvent)
	existing := h.typingMap[roomID]
	if existing == next {
		return
	}
	h.typingMap[roomID] = next
	// we don't persist this for long term storage as typing notifs are inherently ephemeral.
	// So rather than maintaining them forever, they will naturally expire when we terminate.
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2Typing{
		RoomID:         roomID,
		EphemeralEvent: ephEvent,
	})
}

func (h *Handler) OnReceipt(userID, roomID, ephEventType string, ephEvent json.RawMessage) {
	// update our records - we make an artifically new RR event if there are genuine changes
	// else it returns nil
	newReceipts, err := h.Store.ReceiptTable.Insert(roomID, ephEvent)
	if err != nil {
		logger.Err(err).Str("room", roomID).Msg("failed to store receipts")
		sentry.CaptureException(err)
		return
	}
	if len(newReceipts) == 0 {
		return
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2Receipt{
		RoomID:   roomID,
		Receipts: newReceipts,
	})
}

func (h *Handler) AddToDeviceMessages(userID, deviceID string, msgs []json.RawMessage) {
	_, err := h.Store.ToDeviceTable.InsertMessages(userID, deviceID, msgs)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("device", deviceID).Int("msgs", len(msgs)).Msg("V2: failed to store to-device messages")
		sentry.CaptureException(err)
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2DeviceMessages{
		UserID:   userID,
		DeviceID: deviceID,
	})
}

func (h *Handler) UpdateUnreadCounts(roomID, userID string, highlightCount, notifCount *int) {
	// only touch the DB and notify if they have changed. sync v2 will alwyas include the counts
	// even if they haven't changed :(
	key := roomID + userID
	entry, ok := h.unreadMap[key]
	hc := 0
	if highlightCount != nil {
		hc = *highlightCount
	}
	nc := 0
	if notifCount != nil {
		nc = *notifCount
	}
	if ok && entry.Highlight == hc && entry.Notif == nc {
		return // dupe
	}
	h.unreadMap[key] = struct {
		Highlight int
		Notif     int
	}{
		Highlight: hc,
		Notif:     nc,
	}

	err := h.Store.UnreadTable.UpdateUnreadCounters(userID, roomID, highlightCount, notifCount)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to update unread counters")
		sentry.CaptureException(err)
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2UnreadCounts{
		RoomID:            roomID,
		UserID:            userID,
		HighlightCount:    highlightCount,
		NotificationCount: notifCount,
	})
}

func (h *Handler) OnAccountData(userID, roomID string, events []json.RawMessage) {
	data, err := h.Store.InsertAccountData(userID, roomID, events)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to update account data")
		sentry.CaptureException(err)
		return
	}
	var types []string
	for _, d := range data {
		types = append(types, d.Type)
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2AccountData{
		UserID: userID,
		RoomID: roomID,
		Types:  types,
	})
}

func (h *Handler) OnInvite(userID, roomID string, inviteState []json.RawMessage) {
	err := h.Store.InvitesTable.InsertInvite(userID, roomID, inviteState)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to insert invite")
		sentry.CaptureException(err)
		return
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2InviteRoom{
		UserID: userID,
		RoomID: roomID,
	})
}

func (h *Handler) OnLeftRoom(userID, roomID string) {
	// remove any invites for this user if they are rejecting an invite
	err := h.Store.InvitesTable.RemoveInvite(userID, roomID)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to retire invite")
		sentry.CaptureException(err)
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2LeaveRoom{
		UserID: userID,
		RoomID: roomID,
	})
}

func (h *Handler) EnsurePolling(p *pubsub.V3EnsurePolling) {
	log := logger.With().Str("user_id", p.UserID).Str("device_id", p.DeviceID).Logger()
	log.Info().Msg("EnsurePolling: new request")
	defer func() {
		log.Info().Msg("EnsurePolling: request finished")
	}()
	accessToken, since, err := h.v2Store.TokensTable.GetTokenAndSince(p.UserID, p.DeviceID, p.AccessTokenHash)
	if err != nil {
		log.Err(err).Msg("V3Sub: EnsurePolling unknown device")
		sentry.CaptureException(err)
		return
	}
	// don't block us from consuming more pubsub messages just because someone wants to sync
	go func() {
		// blocks until an initial sync is done
		pid := sync2.PollerID{
			UserID:   p.UserID,
			DeviceID: p.DeviceID,
		}
		h.pMap.EnsurePolling(
			pid, accessToken, since, false, log,
		)
		h.updateMetrics()
		h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2InitialSyncComplete{
			UserID:   p.UserID,
			DeviceID: p.DeviceID,
		})
	}()
}

func typingHash(ephEvent json.RawMessage) uint64 {
	h := fnv.New64a()
	for _, userID := range gjson.ParseBytes(ephEvent).Get("content.user_ids").Array() {
		h.Write([]byte(userID.Str))
	}
	return h.Sum64()
}
