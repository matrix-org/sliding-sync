package handler2

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sync"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"

	"github.com/getsentry/sentry-go"

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

// Handler is responsible for starting v2 pollers at startup;
// processing v2 data (as a sync2.V2DataReceiver) and publishing updates (pubsub.Payload to V2Listeners);
// and receiving and processing EnsurePolling events.
type Handler struct {
	pMap      sync2.IPollerMap
	v2Store   *sync2.Storage
	Store     *state.Storage
	v2Pub     pubsub.Notifier
	v3Sub     *pubsub.V3Sub
	unreadMap map[string]struct {
		Highlight int
		Notif     int
	}
	// room_id => fnv_hash([typing user ids])
	typingMap map[string]uint64

	deviceDataTicker *sync2.DeviceDataTicker
	e2eeWorkerPool   *internal.WorkerPool

	numPollers prometheus.Gauge
	subSystem  string
}

func NewHandler(
	pMap *sync2.PollerMap, v2Store *sync2.Storage, store *state.Storage,
	pub pubsub.Notifier, sub pubsub.Listener, enablePrometheus bool, deviceDataUpdateDuration time.Duration,
) (*Handler, error) {
	h := &Handler{
		pMap:      pMap,
		v2Store:   v2Store,
		Store:     store,
		subSystem: "poller",
		unreadMap: make(map[string]struct {
			Highlight int
			Notif     int
		}),
		typingMap:        make(map[string]uint64),
		deviceDataTicker: sync2.NewDeviceDataTicker(deviceDataUpdateDuration),
		e2eeWorkerPool:   internal.NewWorkerPool(500), // TODO: assign as fraction of db max conns, not hardcoded
	}

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
	h.e2eeWorkerPool.Start()
	h.deviceDataTicker.SetCallback(h.OnBulkDeviceDataUpdate)
	go h.deviceDataTicker.Run()
}

func (h *Handler) Teardown() {
	// stop polling and tear down DB conns
	h.v3Sub.Teardown()
	h.v2Pub.Close()
	h.Store.Teardown()
	h.v2Store.Teardown()
	h.pMap.Terminate()
	h.deviceDataTicker.Stop()
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

func (h *Handler) OnTerminated(ctx context.Context, userID, deviceID string) {
	h.updateMetrics()
}

func (h *Handler) OnExpiredToken(ctx context.Context, accessTokenHash, userID, deviceID string) {
	err := h.v2Store.TokensTable.Delete(accessTokenHash)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("device", deviceID).Str("access_token_hash", accessTokenHash).Msg("V2: failed to expire token")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
	}
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
func (h *Handler) UpdateDeviceSince(ctx context.Context, userID, deviceID, since string) {
	err := h.v2Store.DevicesTable.UpdateDeviceSince(userID, deviceID, since)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("device", deviceID).Str("since", since).Msg("V2: failed to persist since token")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
	}
}

func (h *Handler) OnE2EEData(ctx context.Context, userID, deviceID string, otkCounts map[string]int, fallbackKeyTypes []string, deviceListChanges map[string]int) {
	var wg sync.WaitGroup
	wg.Add(1)
	h.e2eeWorkerPool.Queue(func() {
		defer wg.Done()
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
		_, err := h.Store.DeviceDataTable.Upsert(&partialDD)
		if err != nil {
			logger.Err(err).Str("user", userID).Msg("failed to upsert device data")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
			return
		}
		// remember this to notify on pubsub later
		h.deviceDataTicker.Remember(sync2.PollerID{
			UserID:   userID,
			DeviceID: deviceID,
		})
	})
	wg.Wait()
}

// Called periodically by deviceDataTicker, contains many updates
func (h *Handler) OnBulkDeviceDataUpdate(payload *pubsub.V2DeviceData) {
	h.v2Pub.Notify(pubsub.ChanV2, payload)
}

func (h *Handler) Accumulate(ctx context.Context, userID, deviceID, roomID, prevBatch string, timeline []json.RawMessage) {
	// Remember any transaction IDs that may be unique to this user
	eventIDsWithTxns := make([]string, 0, len(timeline))     // in timeline order
	eventIDToTxnID := make(map[string]string, len(timeline)) // event_id -> txn_id
	for _, e := range timeline {
		txnID := gjson.GetBytes(e, "unsigned.transaction_id")
		if !txnID.Exists() {
			continue
		}
		eventID := gjson.GetBytes(e, "event_id").Str
		eventIDsWithTxns = append(eventIDsWithTxns, eventID)
		eventIDToTxnID[eventID] = txnID.Str
	}
	if len(eventIDToTxnID) > 0 {
		// persist the txn IDs
		err := h.Store.TransactionsTable.Insert(userID, deviceID, eventIDToTxnID)
		if err != nil {
			logger.Err(err).Str("user", userID).Str("device", deviceID).Int("num_txns", len(eventIDToTxnID)).Msg("failed to persist txn IDs for user")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		}
	}

	// Insert new events
	numNew, latestNIDs, err := h.Store.Accumulate(roomID, prevBatch, timeline)
	if err != nil {
		logger.Err(err).Int("timeline", len(timeline)).Str("room", roomID).Msg("V2: failed to accumulate room")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
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
	})

	if len(eventIDToTxnID) > 0 {
		// The call to h.Store.Accumulate above only tells us about new events' NIDS;
		// for existing events we need to requery the database to fetch them.
		// Rather than try to reuse work, keep things simple and just fetch NIDs for
		// all events with txnIDs.
		var nidsByIDs map[string]int64
		err = sqlutil.WithTransaction(h.Store.DB, func(txn *sqlx.Tx) error {
			nidsByIDs, err = h.Store.EventsTable.SelectNIDsByIDs(txn, eventIDsWithTxns)
			return err
		})
		if err != nil {
			logger.Err(err).
				Int("timeline", len(timeline)).
				Int("num_transaction_ids", len(eventIDsWithTxns)).
				Str("room", roomID).
				Msg("V2: failed to fetch nids for events with transaction_ids")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
			return
		}

		for _, eventID := range eventIDsWithTxns {
			txnID, ok := eventIDToTxnID[eventID]
			if !ok {
				continue
			}
			nid, ok := nidsByIDs[eventID]
			if !ok {
				errMsg := "V2: failed to fetch NID for txnID"
				logger.Error().Str("user", userID).Str("device", deviceID).Msg(errMsg)
				internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(fmt.Errorf("errMsg"))
				continue
			}

			h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2TransactionID{
				EventID:       eventID,
				UserID:        userID,
				DeviceID:      deviceID,
				TransactionID: txnID,
				NID:           nid,
			})
		}
	}
}

func (h *Handler) Initialise(ctx context.Context, roomID string, state []json.RawMessage) []json.RawMessage {
	res, err := h.Store.Initialise(roomID, state)
	if err != nil {
		logger.Err(err).Int("state", len(state)).Str("room", roomID).Msg("V2: failed to initialise room")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
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

func (h *Handler) SetTyping(ctx context.Context, roomID string, ephEvent json.RawMessage) {
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

func (h *Handler) OnReceipt(ctx context.Context, userID, roomID, ephEventType string, ephEvent json.RawMessage) {
	// update our records - we make an artifically new RR event if there are genuine changes
	// else it returns nil
	newReceipts, err := h.Store.ReceiptTable.Insert(roomID, ephEvent)
	if err != nil {
		logger.Err(err).Str("room", roomID).Msg("failed to store receipts")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
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

func (h *Handler) AddToDeviceMessages(ctx context.Context, userID, deviceID string, msgs []json.RawMessage) {
	_, err := h.Store.ToDeviceTable.InsertMessages(userID, deviceID, msgs)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("device", deviceID).Int("msgs", len(msgs)).Msg("V2: failed to store to-device messages")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2DeviceMessages{
		UserID:   userID,
		DeviceID: deviceID,
	})
}

func (h *Handler) UpdateUnreadCounts(ctx context.Context, roomID, userID string, highlightCount, notifCount *int) {
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
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2UnreadCounts{
		RoomID:            roomID,
		UserID:            userID,
		HighlightCount:    highlightCount,
		NotificationCount: notifCount,
	})
}

func (h *Handler) OnAccountData(ctx context.Context, userID, roomID string, events []json.RawMessage) {
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

func (h *Handler) OnInvite(ctx context.Context, userID, roomID string, inviteState []json.RawMessage) {
	err := h.Store.InvitesTable.InsertInvite(userID, roomID, inviteState)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to insert invite")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		return
	}
	h.v2Pub.Notify(pubsub.ChanV2, &pubsub.V2InviteRoom{
		UserID: userID,
		RoomID: roomID,
	})
}

func (h *Handler) OnLeftRoom(ctx context.Context, userID, roomID string) {
	// remove any invites for this user if they are rejecting an invite
	err := h.Store.InvitesTable.RemoveInvite(userID, roomID)
	if err != nil {
		logger.Err(err).Str("user", userID).Str("room", roomID).Msg("failed to retire invite")
		internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
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
