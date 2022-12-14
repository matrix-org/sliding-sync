package pubsub

import (
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Every payload needs a type to distinguish what kind of update it is.
type Payload interface {
	Type() string
}

// Listener represents the common functions required by all subscription listeners
type Listener interface {
	// Begin listening on this channel with this callback starting from this position. Blocks until Close() is called.
	Listen(chanName string, fn func(p Payload)) error
	// Close the listener. No more callbacks should fire.
	Close() error
}

// Notifier represents the common functions required by all notifiers
type Notifier interface {
	// Notify chanName that there is a new payload p. Return an error if we failed to send the notification.
	Notify(chanName string, p Payload) error
	// Close is called when we should stop listening.
	Close() error
}

type PubSub struct {
	chans      map[string]chan Payload
	mu         *sync.Mutex
	closed     bool
	bufferSize int
}

func NewPubSub(bufferSize int) *PubSub {
	return &PubSub{
		chans:      make(map[string]chan Payload),
		mu:         &sync.Mutex{},
		bufferSize: bufferSize,
	}
}

func (ps *PubSub) getChan(chanName string) chan Payload {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ch := ps.chans[chanName]
	if ch == nil {
		ch = make(chan Payload, ps.bufferSize)
		ps.chans[chanName] = ch
	}
	return ch
}

func (ps *PubSub) Notify(chanName string, p Payload) error {
	ch := ps.getChan(chanName)
	select {
	case ch <- p:
		break
	case <-time.After(5 * time.Second):
		return fmt.Errorf("notify with payload %v timed out", p.Type())
	}
	return nil
}

func (ps *PubSub) Close() error {
	if ps.closed {
		return nil
	}
	ps.closed = true
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, ch := range ps.chans {
		close(ch)
	}
	return nil
}

func (ps *PubSub) Listen(chanName string, fn func(p Payload)) error {
	ch := ps.getChan(chanName)
	for payload := range ch {
		fn(payload)
	}
	return nil
}

// Wrapper around a Notifier which adds Prometheus metrics
type PromNotifier struct {
	Notifier
	msgCounter *prometheus.CounterVec
}

func (p *PromNotifier) Notify(chanName string, payload Payload) error {
	p.msgCounter.WithLabelValues(payload.Type()).Inc()
	return p.Notifier.Notify(chanName, payload)
}

func (p *PromNotifier) Close() error {
	prometheus.Unregister(p.msgCounter)
	return p.Notifier.Close()
}

// Wrap a notifier for prometheus metrics
func NewPromNotifier(n Notifier, subsystem string) Notifier {
	p := &PromNotifier{
		Notifier: n,
		msgCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "sliding_sync",
			Subsystem: subsystem,
			Name:      "num_payloads",
			Help:      "Number of payloads published",
		}, []string{"payload_type"}),
	}
	prometheus.MustRegister(p.msgCounter)
	return p
}
