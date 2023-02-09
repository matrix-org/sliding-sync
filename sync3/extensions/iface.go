package extensions

import (
	"context"

	"github.com/matrix-org/sliding-sync/sync3/caches"
)

type GenericRequest interface {
	Name() string
	// Returns the value of the `enabled` JSON key. nil for "not specified".
	IsEnabled() *bool
	// Overwrite fields in the request by side-effecting on this struct.
	ApplyDelta(next GenericRequest)
	// Process this request and put the response into *Response.
	ProcessInitial(ctx context.Context, res *Response, extCtx Context)
	// Process a live event, /aggregating/ the response in *Response. This function can be called
	// multiple times per sync loop as the conn buffer is consumed.
	AppendLive(ctx context.Context, res *Response, extCtx Context, up caches.Update)
}

// mixin for managing the enabled flag
type Enableable struct {
	Enabled *bool `json:"enabled"`
}

func (r *Enableable) Name() string {
	return "Enableable"
}

func (r *Enableable) IsEnabled() *bool {
	return r.Enabled
}

func (r *Enableable) ApplyDelta(gnext GenericRequest) {
	if gnext == nil {
		return
	}
	nextEnabled := gnext.IsEnabled()
	// nil means they didn't specify this field, so leave it unchanged.
	if nextEnabled != nil {
		r.Enabled = nextEnabled
	}
}

func ExtensionEnabled(r GenericRequest) bool {
	enabled := r.IsEnabled()
	if enabled != nil && *enabled {
		return true
	}
	return false
}
