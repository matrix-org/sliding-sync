package pubsub

// The channel which has V3* payloads
const ChanV3 = "v3ch"

type V3Listener interface {
	EnsurePolling(p *V3EnsurePolling)
}

type V3EnsurePolling struct {
	UserID   string
	DeviceID string
}

func (v V3EnsurePolling) Type() string { return "p" }

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
	switch p.Type() {
	case V3EnsurePolling{}.Type():
		v.receiver.EnsurePolling(p.(*V3EnsurePolling))
	}
}

func (v *V3Sub) Listen() error {
	return v.listener.Listen(ChanV3, v.onMessage)
}
