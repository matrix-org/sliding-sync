package sync3

import "github.com/matrix-org/sync-v3/sync2"

// A Session represents a single device's sync stream. One device can have many sessions open at
// once. Sessions are created when devices sync without a since token. Sessions are destroyed
// after a configurable period of inactivity.
type Session struct {
	V2                   *sync2.Device
	ID                   int64  `db:"session_id"`
	LastConfirmedToken   string `db:"last_confirmed_token"`
	LastUnconfirmedToken string `db:"last_unconfirmed_token"`
}
