package extensions

import (
	"context"
	"os"
	"runtime/trace"

	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/matrix-org/sliding-sync/sync3/delta"
	"github.com/rs/zerolog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type Request struct {
	UserID      string
	DeviceID    string
	ToDevice    *ToDeviceRequest    `json:"to_device"`
	E2EE        *E2EERequest        `json:"e2ee"`
	AccountData *AccountDataRequest `json:"account_data"`
	Typing      *TypingRequest      `json:"typing"`
	Receipts    *ReceiptsRequest    `json:"receipts"`
}

func (r Request) ApplyDelta(next *Request) Request {
	if next.ToDevice != nil {
		r.ToDevice = r.ToDevice.ApplyDelta(next.ToDevice)
	}
	if next.E2EE != nil {
		r.E2EE = r.E2EE.ApplyDelta(next.E2EE)
	}
	if next.AccountData != nil {
		r.AccountData = r.AccountData.ApplyDelta(next.AccountData)
	}
	if next.Typing != nil {
		r.Typing = r.Typing.ApplyDelta(next.Typing)
	}
	if next.Receipts != nil {
		r.Receipts = r.Receipts.ApplyDelta(next.Receipts)
	}
	return r
}

type Response struct {
	ToDevice    *ToDeviceResponse    `json:"to_device,omitempty"`
	E2EE        *E2EEResponse        `json:"e2ee,omitempty"`
	AccountData *AccountDataResponse `json:"account_data,omitempty"`
	Typing      *TypingResponse      `json:"typing,omitempty"`
	Receipts    *ReceiptsResponse    `json:"receipts,omitempty"`
}

func (e Response) HasData(isInitial bool) bool {
	return (e.ToDevice != nil && e.ToDevice.HasData(isInitial)) ||
		(e.E2EE != nil && e.E2EE.HasData(isInitial)) ||
		(e.AccountData != nil && e.AccountData.HasData(isInitial))
}

type HandlerInterface interface {
	Handle(ctx context.Context, req Request, deltaData *delta.State, roomIDToTimeline map[string][]string, isInitial bool) (res Response)
	HandleLiveUpdate(update caches.Update, req Request, res *Response, deltaData *delta.State, updateWillReturnResponse, isInitial bool)
}

type Handler struct {
	Store       *state.Storage
	E2EEFetcher E2EEFetcher
	GlobalCache *caches.GlobalCache
}

func (h *Handler) HandleLiveUpdate(update caches.Update, req Request, res *Response, deltaData *delta.State, updateWillReturnResponse, isInitial bool) {
	if req.AccountData != nil && req.AccountData.Enabled {
		res.AccountData = ProcessLiveAccountData(update, h.Store, deltaData, updateWillReturnResponse, req.UserID, req.AccountData)
	}
	if req.Typing != nil && req.Typing.Enabled {
		res.Typing = ProcessLiveTyping(update, updateWillReturnResponse, req.UserID, req.Typing)
	}
	if req.Receipts != nil && req.Receipts.Enabled {
		res.Receipts = ProcessLiveReceipts(update, updateWillReturnResponse, req.UserID, req.Receipts)
	}
	if req.ToDevice != nil && req.ToDevice.Enabled != nil && *req.ToDevice.Enabled {
		res.ToDevice = ProcessLiveToDeviceEvents(update, h.Store, req.UserID, req.DeviceID, req.ToDevice)
	}
	// only process 'live' e2ee when we aren't going to return data as we need to ensure that we don't calculate this twice
	// e.g once on incoming request then again due to wakeup
	if req.E2EE != nil && req.E2EE.Enabled {
		if res.E2EE != nil && res.E2EE.HasData(false) {
			return
		}
		res.E2EE = ProcessLiveE2EE(update, h.E2EEFetcher, req.UserID, req.DeviceID, req.E2EE)
	}
}

func (h *Handler) Handle(ctx context.Context, req Request, deltaData *delta.State, roomIDToTimeline map[string][]string, isInitial bool) (res Response) {
	if req.ToDevice != nil && req.ToDevice.Enabled != nil && *req.ToDevice.Enabled {
		region := trace.StartRegion(ctx, "extension_to_device")
		res.ToDevice = ProcessToDevice(h.Store, req.UserID, req.DeviceID, req.ToDevice, isInitial)
		region.End()
	}
	if req.E2EE != nil && req.E2EE.Enabled {
		region := trace.StartRegion(ctx, "extension_e2ee")
		res.E2EE = ProcessE2EE(h.E2EEFetcher, req.UserID, req.DeviceID, req.E2EE, isInitial)
		region.End()
	}
	if req.AccountData != nil && req.AccountData.Enabled {
		region := trace.StartRegion(ctx, "extension_account_data")
		res.AccountData = ProcessAccountData(h.Store, deltaData, roomIDToTimeline, req.UserID, isInitial, req.AccountData)
		region.End()
	}
	if req.Typing != nil && req.Typing.Enabled {
		region := trace.StartRegion(ctx, "extension_typing")
		res.Typing = ProcessTyping(h.GlobalCache, roomIDToTimeline, req.UserID, isInitial, req.Typing)
		region.End()
	}
	if req.Receipts != nil && req.Receipts.Enabled {
		region := trace.StartRegion(ctx, "extension_receipts")
		res.Receipts = ProcessReceipts(h.Store, roomIDToTimeline, req.UserID, isInitial, req.Receipts)
		region.End()
	}
	return
}
