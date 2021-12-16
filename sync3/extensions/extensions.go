package extensions

import (
	"os"

	"github.com/matrix-org/sync-v3/state"
	"github.com/matrix-org/sync-v3/sync2"
	"github.com/rs/zerolog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type Request struct {
	UserID   string
	DeviceID string
	ToDevice ToDeviceRequest `json:"to_device"`
	E2EE     E2EERequest     `json:"e2ee"`
}

type Response struct {
	ToDevice *ToDeviceResponse `json:"to_device,omitempty"`
	E2EE     *E2EEResponse     `json:"e2ee,omitempty"`
}

func (e Response) HasData() bool {
	return (e.ToDevice != nil && e.ToDevice.HasData()) || (e.E2EE != nil && e.E2EE.HasData())
}

type HandlerInterface interface {
	Handle(req Request) (res Response)
}

type Handler struct {
	Store       *state.Storage
	E2EEFetcher sync2.E2EEFetcher
}

func (h *Handler) Handle(req Request) (res Response) {
	if req.ToDevice.Enabled {
		res.ToDevice = ProcessToDevice(h.Store, req.UserID, req.DeviceID, &req.ToDevice)
	}
	if req.E2EE.Enabled {
		res.E2EE = ProcessE2EE(h.E2EEFetcher, req.UserID, req.DeviceID, &req.E2EE)
	}
	return
}
