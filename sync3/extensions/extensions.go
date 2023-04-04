// package extensions contains the interface and concrete implementations for all sliding sync extensions
package extensions

import (
	"context"
	"os"
	"reflect"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync3/caches"
	"github.com/rs/zerolog"
)

var logger = zerolog.New(os.Stdout).With().Timestamp().Logger().Output(zerolog.ConsoleWriter{
	Out:        os.Stderr,
	TimeFormat: "15:04:05",
})

type GenericRequest interface {
	// Name provides a name to identify the kind of request. At present, it's only
	// used to name opentracing spans; this isn't end-user visible.
	Name() string
	// Returns the value of the `enabled` JSON key. nil for "not specified".
	IsEnabled() *bool
	// Returns the value of the `lists` JSON key. nil for "not specified".
	OnlyLists() []string
	// Returns the value of the `rooms` JSON key. nil for "not specified".
	OnlyRooms() []string
	// Overwrite fields in the request by side-effecting on this struct.
	ApplyDelta(next GenericRequest)
	// ProcessInitial provides a means for extensions to return data to clients immediately.
	// If an implementation wants to do so, put the response into *Response (and ensure
	// that GenericResponse.HasData returns true).
	//
	// This is called for every request before going into a live stream loop. By "every
	// request" we mean both initial and incremental syncs; this function gets call
	// multiple times over the lifetime of a connection. (Read Context.IsInitial to see
	// if we are serving an initial or incremental sync.)
	ProcessInitial(ctx context.Context, res *Response, extCtx Context)
	// Process a live event, /aggregating/ the response in *Response. This function can be called
	// multiple times per sync loop as the conn buffer is consumed.
	AppendLive(ctx context.Context, res *Response, extCtx Context, up caches.Update)
}

type GenericResponse interface {
	// HasData determines if there is enough data in the response for it to be
	// meaningful or useful for the client. If so, we eagerly return this response
	// (even if we are aware of other updates). The parameter isInitial is true if and
	// only if this response is to an initial sync request; this allows the
	// implementation to send out a "fuller" update to an initial sync than it would
	// for an incremental sync.
	HasData(isInitial bool) bool
}

// mixin for managing the flags reserved by the Core API
type Core struct {
	// All fields are optional, with nil meaning "not specified".
	Enabled *bool    `json:"enabled"`
	Lists   []string `json:"lists"`
	Rooms   []string `json:"rooms"`
}

func (r *Core) Name() string {
	return "Core"
}

func (r *Core) IsEnabled() *bool {
	return r.Enabled
}

func (r *Core) OnlyLists() []string {
	return r.Lists
}

func (r *Core) OnlyRooms() []string {
	return r.Rooms
}

func (r *Core) ApplyDelta(gnext GenericRequest) {
	if gnext == nil {
		return
	}
	nextEnabled := gnext.IsEnabled()
	// nil means they didn't specify this field, so leave it unchanged.
	if nextEnabled != nil {
		r.Enabled = nextEnabled
	}
	nextLists := gnext.OnlyLists()
	if nextLists != nil {
		r.Lists = nextLists
	}
	nextRooms := gnext.OnlyRooms()
	if nextRooms != nil {
		r.Rooms = nextRooms
	}
}

// RoomInScope determines whether a given room ought to be processed by this extension,
// according to the "core" extension scoping logic. Extensions are free to suppress
// updates for a room based on additional criteria.
func (r *Core) RoomInScope(roomID string, extCtx Context) bool {
	// If the extension hasn't had its scope configured, process everything.
	if r.Lists == nil && r.Rooms == nil {
		return true
	}

	// If this extension has been explicitly subscribed to this room, process the update.
	for _, roomInScope := range r.Rooms {
		if roomInScope == roomID {
			return true
		}
	}

	// If the room belongs to one of the lists that this extension should process, process the update.
	visibleInLists := extCtx.RoomIDsToLists[roomID]
	for _, visibleInList := range visibleInLists {
		for _, shouldProcessList := range r.Lists {
			if visibleInList == shouldProcessList {
				return true
			}
		}
	}

	// Otherwise ignore the update.
	return false
}

func ExtensionEnabled(r GenericRequest) bool {
	enabled := r.IsEnabled()
	if enabled != nil && *enabled {
		return true
	}
	return false
}

// Request is the JSON request body under 'extensions'.
//
// To add new extensions, add a field here and return it in fields() whilst setting it correctly
// in setFields().
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
//
// To add a new extension, add a field here and in fields().
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
	// RoomIDToTimeline is a map from room IDs to slices of event IDs. The keys are the
	// rooms which
	// - have seen new events since the last sync request, and
	// - the client is subscribed to (via a list or a room subscription).
	// The values are the event IDs at the end of the room "timeline", ordered from
	// oldest to newest, as determined by the core sliding sync protocol.
	// TODO: can the timelines be "gappy" like a v2 sync timeline?
	RoomIDToTimeline map[string][]string
	// IsInitial is true if this sync is requesting a snapshot of current client state
	// (pos = 0, "initial sync") and false otherwise (pos > 0, "incremental sync").
	IsInitial bool
	UserID    string
	DeviceID  string
	// Map from room IDs to list names. Keys are the room IDs of all rooms currently
	// visible in at least one sliding window. Values are the names of the lists that
	// enclose those sliding windows. Values should be nonnil and nonempty, and may
	// contain multiple list names.
	RoomIDsToLists map[string][]string
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
		childCtx, region := internal.StartSpan(ctx, "extension_"+ext.Name())
		ext.ProcessInitial(childCtx, &res, extCtx)
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
