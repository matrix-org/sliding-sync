package synclive

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/rs/zerolog/hlog"
)

type SyncLiveHandler struct {
	V2        sync2.Client
	Storage   *state.Storage
	V2Store   *sync2.Storage
	PollerMap *sync2.PollerMap
	Notifier  *Notifier
}

func NewSyncLiveHandler(v2Client sync2.Client, postgresDBURI string) *SyncLiveHandler {
	sh := &SyncLiveHandler{
		V2:      v2Client,
		Storage: state.NewStorage(postgresDBURI),
		V2Store: sync2.NewStore(postgresDBURI),
	}
	sh.PollerMap = sync2.NewPollerMap(v2Client, sh)

	return sh
}

func (h *SyncLiveHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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

func (h *SyncLiveHandler) serve(w http.ResponseWriter, req *http.Request) error {
	// associate this request with a connection or make a new connection
	_, err := h.getOrCreateConnection(req)
	if err != nil {
		return err
	}

	return nil
}

func (h *SyncLiveHandler) getOrCreateConnection(req *http.Request) (*Conn, error) {
	log := hlog.FromRequest(req)
	var conn *Conn

	// Identify the device
	deviceID, err := internal.DeviceIDFromRequest(req)
	if err != nil {
		log.Warn().Err(err).Msg("failed to get device ID from request")
		return nil, &internal.HandlerError{
			StatusCode: 400,
			Err:        err,
		}
	}
	sessionID := req.URL.Query().Get("session")

	// client thinks they have a connection
	if sessionID != "" {
		// Lookup the connection
		// we need to map based on both as the session ID isn't crypto secure but the device ID is (Auth header)
		conn = h.Notifier.Conn(ConnID{
			SessionID: sessionID,
			DeviceID:  deviceID,
		})
		if err != nil {
			log.Warn().Err(err).Msg("failed to lookup conn for request")
			return nil, &internal.HandlerError{
				StatusCode: 400,
				Err:        err,
			}
		}
		if conn != nil {
			// conn exists

			// TODO: Wrap in OnIncomingRequest(http.Request)?
			cpos, err := strconv.ParseInt(req.URL.Query().Get("pos"), 10, 64)
			if err != nil {
				return nil, &internal.HandlerError{
					StatusCode: 400,
					Err:        fmt.Errorf("invalid position: %s", req.URL.Query().Get("pos")),
				}
			}
			var body []byte
			if req.Body != nil {
				body, err = ioutil.ReadAll(req.Body)
				if err != nil {
					return nil, &internal.HandlerError{
						StatusCode: 400,
						Err:        err,
					}
				}
			}
			conn.OnIncomingRequest(req.Context(), cpos, body)
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

	// Now the v2 side of things are running, we can make a v3 live sync conn
	return h.createConn(ConnID{
		SessionID: h.generateSessionID(),
		DeviceID:  deviceID,
	})
}

func (h *SyncLiveHandler) createConn(connID ConnID) (*Conn, error) {
	// TODO register the connection with the notifier
	conn := NewConn(connID)
	h.Notifier.SetConn(conn)
	return conn, nil
}

func (h *SyncLiveHandler) generateSessionID() string {
	return "1"
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) UpdateDeviceSince(deviceID, since string) error {
	return h.V2Store.UpdateDeviceSince(deviceID, since)
}

// Called from the v2 poller, implements V2DataReceiver
func (h *SyncLiveHandler) Accumulate(roomID string, timeline []json.RawMessage) error {
	_, err := h.Storage.Accumulate(roomID, timeline)
	return err
}

func (h *SyncLiveHandler) Initialise(roomID string, state []json.RawMessage) error {
	_, err := h.Storage.Initialise(roomID, state)
	return err
}

func (h *SyncLiveHandler) SetTyping(roomID string, userIDs []string) (int64, error) {
	return h.Storage.TypingTable.SetTyping(roomID, userIDs)
}

// Add messages for this device. If an error is returned, the poll loop is terminated as continuing
// would implicitly acknowledge these messages.
func (h *SyncLiveHandler) AddToDeviceMessages(userID, deviceID string, msgs []gomatrixserverlib.SendToDeviceEvent) error {
	_, err := h.Storage.ToDeviceTable.InsertMessages(deviceID, msgs)
	return err
}
