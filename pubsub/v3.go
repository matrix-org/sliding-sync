package pubsub

import (
	"github.com/rs/zerolog/log"
)

// The channel which has V3* payloads
const ChanV3 = "v3ch"

// V3Listener describes the messages that incoming sliding sync requests will publish.
type V3Listener interface {
	EnsurePolling(p *V3EnsurePolling)
}

type V3EnsurePolling struct {
	// TODO: we only really need to provide the access token hash here.
	// Passing through a user means we can log something sensible though.
	UserID          string
	DeviceID        string
	AccessTokenHash string
}

func (*V3EnsurePolling) Type() string { return "V3EnsurePolling" }

type V3Sub struct {
	listener Listener
	receiver V3Listener
}

func NewV3Sub(l Listener, recv V3Listener) *V3Sub {
	return &V3Sub{
		listener: l,
		receiver: recv,
	}
}

func (v *V3Sub) Teardown() {
	v.listener.Close()
}

func (v *V3Sub) onMessage(p Payload) {
	switch pl := p.(type) {
	case *V3EnsurePolling:
		v.receiver.EnsurePolling(pl)
	default:
		log.Warn().Str("type", p.Type()).Msg("V3Sub: unhandled payload type")
	}
}

func (v *V3Sub) Listen() error {
	return v.listener.Listen(ChanV3, v.onMessage)
}
