package extensions

import (
	"fmt"
	"os"

	"github.com/matrix-org/sync-v3/state"
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
}

type Response struct {
	ToDevice *ToDeviceResponse `json:"to_device,omitempty"`
}

func (e Response) HasData() bool {
	return (e.ToDevice != nil && e.ToDevice.HasData())
}

type HandlerInterface interface {
	Handle(req Request) (res Response)
}

type Handler struct {
	Store *state.Storage
}

func (h *Handler) Handle(req Request) (res Response) {
	fmt.Printf("%+v\n", req)
	if req.ToDevice.Enabled {
		res.ToDevice = ProcessToDevice(h.Store, req.UserID, req.DeviceID, &req.ToDevice)
	}
	return
}
