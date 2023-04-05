package internal

import (
	"context"
	"github.com/getsentry/sentry-go"
)

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
