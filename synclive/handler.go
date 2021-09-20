package observables

import (
	"encoding/json"
	"net/http"

	"github.com/matrix-org/gomatrixserverlib"
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync2"
)

type SyncLiveHandler struct {
	V2        sync2.Client
	Storage   *state.Storage
	V2Store   *sync2.Storage
	PollerMap *sync2.PollerMap
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
		w.WriteHeader(err.StatusCode)
		w.Write(err.JSON())
	}
}

func (h *SyncLiveHandler) serve(w http.ResponseWriter, req *http.Request) *internal.HandlerError {
	return nil
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
