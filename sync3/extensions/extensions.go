package extensions

import (
	"context"
	"os"
	"runtime/trace"

	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/rs/zerolog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type Request struct {
	ToDevice    *ToDeviceRequest    `json:"to_device"`
	E2EE        *E2EERequest        `json:"e2ee"`
	AccountData *AccountDataRequest `json:"account_data"`
	Typing      *TypingRequest      `json:"typing"`
	Receipts    *ReceiptsRequest    `json:"receipts"`
}

func (r Request) EnabledExtensions() (exts []GenericRequest) {
	if r.AccountData != nil && ExtensionEnabled(r.AccountData) {
		exts = append(exts, r.AccountData)
	}
	if r.E2EE != nil && ExtensionEnabled(r.E2EE) {
		exts = append(exts, r.E2EE)
	}
	if r.Receipts != nil && ExtensionEnabled(r.Receipts) {
		exts = append(exts, r.Receipts)
	}
	if r.ToDevice != nil && ExtensionEnabled(r.ToDevice) {
		exts = append(exts, r.ToDevice)
	}
	if r.Typing != nil && ExtensionEnabled(r.Typing) {
		exts = append(exts, r.Typing)
	}
	return
}

func (r Request) ApplyDelta(next *Request) Request {
	if next.ToDevice != nil {
		if r.ToDevice == nil {
			r.ToDevice = next.ToDevice
		} else {
			r.ToDevice.ApplyDelta(next.ToDevice)
		}
	}
	if next.E2EE != nil {
		if r.E2EE == nil {
			r.E2EE = next.E2EE
		} else {
			r.E2EE.ApplyDelta(next.E2EE)
		}
	}
	if next.AccountData != nil {
		if r.AccountData == nil {
			r.AccountData = next.AccountData
		} else {
			r.AccountData.ApplyDelta(next.AccountData)
		}
	}
	if next.Typing != nil {
		if r.Typing == nil {
			r.Typing = next.Typing
		} else {
			r.Typing.ApplyDelta(next.Typing)
		}
	}
	if next.Receipts != nil {
		if r.Receipts == nil {
			r.Receipts = next.Receipts
		} else {
			r.Receipts.ApplyDelta(next.Receipts)
		}
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
		(e.AccountData != nil && e.AccountData.HasData(isInitial)) ||
		(e.Receipts != nil && e.Receipts.HasData(isInitial))
}

type Context struct {
	*Handler
	RoomIDToTimeline map[string][]string
	IsInitial        bool
	UserID           string
	DeviceID         string
}

type HandlerInterface interface {
	Handle(ctx context.Context, req Request, extCtx Context) (res Response)
	HandleLiveUpdate(update caches.Update, req Request, res *Response, extCtx Context)
}

type Handler struct {
	Store       *state.Storage
	E2EEFetcher E2EEFetcher
	GlobalCache *caches.GlobalCache
}

func (h *Handler) HandleLiveUpdate(update caches.Update, req Request, res *Response, extCtx Context) {
	extCtx.Handler = h
	exts := req.EnabledExtensions()
	for _, ext := range exts {
		ext.AppendLive(context.Background(), res, extCtx, update)
	}
}

func (h *Handler) Handle(ctx context.Context, req Request, extCtx Context) (res Response) {
	extCtx.Handler = h
	exts := req.EnabledExtensions()
	for _, ext := range exts {
		region := trace.StartRegion(ctx, "extension_"+ext.Name())
		ext.ProcessInitial(ctx, &res, extCtx)
		region.End()
	}
	return
}
