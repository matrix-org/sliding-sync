package sync3

// A Session represents a single device's sync stream. One device can have many sessions open at
// once. Sessions are created when devices sync without a since token. Sessions are destroyed
// after a configurable period of inactivity.
type Session struct {
	ID                   int64  `db:"session_id"`
	UserID               string `db:"user_id"`
	DeviceID             string `db:"device_id"`
	LastConfirmedToken   string `db:"last_confirmed_token"`
	LastUnconfirmedToken string `db:"last_unconfirmed_token"`

	V2Since string `db:"since"`
}
