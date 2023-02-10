package extensions

import (
	"context"
	"os"
	"reflect"
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

func (r *Request) fields() []GenericRequest {
	return []GenericRequest{
		r.ToDevice, r.E2EE, r.AccountData, r.Typing, r.Receipts,
	}
}

// these fields must match up in order/type to fields()
func (r *Request) setFields(fields []GenericRequest) {
	r.ToDevice = fields[0].(*ToDeviceRequest)
	r.E2EE = fields[1].(*E2EERequest)
	r.AccountData = fields[2].(*AccountDataRequest)
	r.Typing = fields[3].(*TypingRequest)
	r.Receipts = fields[4].(*ReceiptsRequest)
}

func (r Request) EnabledExtensions() (exts []GenericRequest) {
	fields := r.fields()
	for _, f := range fields {
		f := f
		if isNil(f) {
			continue
		}
		if ExtensionEnabled(f) {
			exts = append(exts, f)
		}
	}
	return
}

func (r Request) ApplyDelta(next *Request) Request {
	currFields := r.fields()
	nextFields := next.fields()
	hasChanges := false
	for i := range nextFields {
		curr := currFields[i]
		next := nextFields[i]
		if isNil(next) {
			continue // missing extension, keep it sticky
		}
		if isNil(curr) {
			// the next field is what we want to apply
			currFields[i] = next
			hasChanges = true
		} else {
			// mix the two fields together
			curr.ApplyDelta(next)
			hasChanges = true
		}
	}
	if hasChanges {
		r.setFields(currFields)
	}

	return r
}

// Response represents the top-level `extensions` key in the JSON response.
// To add a new extension, add a field here, then modify the fields in HasData.
type Response struct {
	ToDevice    *ToDeviceResponse    `json:"to_device,omitempty"`
	E2EE        *E2EEResponse        `json:"e2ee,omitempty"`
	AccountData *AccountDataResponse `json:"account_data,omitempty"`
	Typing      *TypingResponse      `json:"typing,omitempty"`
	Receipts    *ReceiptsResponse    `json:"receipts,omitempty"`
}

func (r Response) fields() []GenericResponse {
	return []GenericResponse{
		r.ToDevice, r.E2EE, r.AccountData, r.Typing, r.Receipts,
	}
}

func (r Response) HasData(isInitial bool) bool {
	fields := r.fields()
	for _, f := range fields {
		if isNil(f) {
			continue
		}
		if !f.HasData(isInitial) {
			continue
		}
		return true
	}
	return false
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

// check if this interface is pointing to nil, or is itself nil. Nil interfaces can be checked by
// doing == nil but interfaces holding nil pointers cannot, and need to be done using reflection :(
// it's not particularly nice, but is arguably neater than adding nil guards everywhere in method
// functions.
func isNil(v interface{}) bool {
	return v == nil || (reflect.ValueOf(v).Kind() == reflect.Ptr && reflect.ValueOf(v).IsNil())
}
