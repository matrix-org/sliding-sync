package internal

import (
	"context"
	"github.com/getsentry/sentry-go"
	"time"
)

// SentryCtxKey is a namespace under which we can report arbitrary key-value pairs to
// sentry.
const SentryCtxKey = "sliding-sync"

// GetSentryHubFromContextOrDefault is a version of sentry.GetHubFromContext which
// automatically falls back to sentry.CurrentHub if the given context has not been
// attached a hub.
//
// The various golang sentry integrations automatically attach a hub to contexts that
// are generated when serving HTTP requests. If that accounts for all your contexts,
// you have no need for this function; you can use sentry.GetHubFromContext without
// fear.
//
// The returned pointer is always nonnil.
func GetSentryHubFromContextOrDefault(ctx context.Context) *sentry.Hub {
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		hub = sentry.CurrentHub()
	}
	return hub
}

// ReportPanicsToSentry checks for panics by calling recover, reports any panic found to
// sentry, and then reraises the panic. To have tracebacks included in the report, make
// sure you call panic with something that implements error. (Anything else will be
// reported as a "message" rather than an "exception" in Sentry; by default, only
// "exceptions" are reported with tracebacks. See e.g.
//
//	   https://github.com/getsentry/sentry-go/blob/eec094e9470dd3855eaf47b025d853bcbc13df68/client.go#L438-L447
//	for some of the machinery.)
//
// Typically, you want to call this in the form `defer internal.ReportPanicsToSentry()`.
func ReportPanicsToSentry() {
	panicData := recover()
	if panicData != nil {
		sentry.CurrentHub().Recover(panicData)
		sentry.Flush(time.Second * 5)
	}
	// We still want to fail loudly here.
	if panicData != nil {
		panic(panicData)
	}
}
