package extensions

import "github.com/matrix-org/sync-v3/state"

type Request struct {
	ToDevice ToDeviceRequest `json:"to_device"`
}

type Response struct {
	ToDevice *ToDeviceResponse `json:"to_device,omitempty"`
}

func (e Response) HasData() bool {
	return e.ToDevice != nil
}

type HandlerInterface interface {
	Handle(req Request) (res Response)
}

type Handler struct {
	Store *state.Storage
}

func (h *Handler) Handle(req Request) (res Response) {
	return
}
