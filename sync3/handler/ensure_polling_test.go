package handler

import (
	"context"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/pubsub"
	"github.com/matrix-org/sliding-sync/sync2"
)

type mockNotifier struct {
	ch chan pubsub.Payload
}

func (n *mockNotifier) Notify(chanName string, p pubsub.Payload) error {
	n.ch <- p
	return nil
}

func (n *mockNotifier) Close() error {
	return nil
}

func (n *mockNotifier) MustHaveNoSentPayloads(t *testing.T) {
	t.Helper()
	if len(n.ch) == 0 {
		return
	}
	t.Fatalf("MustHaveNoSentPayloads: %d in buffer", len(n.ch))
}

func (n *mockNotifier) WaitForNextPayload(t *testing.T, timeout time.Duration) pubsub.Payload {
	t.Helper()
	select {
	case p := <-n.ch:
		return p
	case <-time.After(timeout):
		t.Fatalf("WaitForNextPayload: timed out after %v", timeout)
	}
	panic("unreachable")
}

// check that the request/response works and unblocks things correctly
func TestEnsurePollerBasicWorks(t *testing.T) {
	n := &mockNotifier{ch: make(chan pubsub.Payload, 100)}
	ctx := context.Background()
	pid := sync2.PollerID{UserID: "@alice:localhost", DeviceID: "DEVICE"}
	tokHash := "tokenHash"
	ep := NewEnsurePoller(n, false)

	var expired atomic.Bool
	finished := make(chan bool) // dummy
	go func() {
		exp := ep.EnsurePolling(ctx, pid, tokHash)
		expired.Store(exp)
		close(finished)
	}()

	p := n.WaitForNextPayload(t, time.Second)

	// check it's a V3EnsurePolling payload
	pp, ok := p.(*pubsub.V3EnsurePolling)
	if !ok {
		t.Fatalf("unexpected payload: %+v", p)
	}
	assertVal(t, pp.UserID, pid.UserID)
	assertVal(t, pp.DeviceID, pid.DeviceID)
	assertVal(t, pp.AccessTokenHash, tokHash)

	// make sure we're still waiting
	select {
	case <-finished:
		t.Fatalf("EnsurePolling unblocked before response was sent")
	default:
	}

	// send back the response
	ep.OnInitialSyncComplete(&pubsub.V2InitialSyncComplete{
		UserID:   pid.UserID,
		DeviceID: pid.DeviceID,
		Success:  true,
	})

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatalf("EnsurePolling didn't unblock after response was sent")
	}

	if expired.Load() {
		t.Fatalf("response said token was expired when it wasn't")
	}
}

func TestEnsurePollerCachesResponses(t *testing.T) {
	n := &mockNotifier{ch: make(chan pubsub.Payload, 100)}
	ctx := context.Background()
	pid := sync2.PollerID{UserID: "@alice:localhost", DeviceID: "DEVICE"}
	ep := NewEnsurePoller(n, false)

	finished := make(chan bool) // dummy
	go func() {
		_ = ep.EnsurePolling(ctx, pid, "tokenHash")
		close(finished)
	}()

	n.WaitForNextPayload(t, time.Second) // wait for V3EnsurePolling
	// send back the response
	ep.OnInitialSyncComplete(&pubsub.V2InitialSyncComplete{
		UserID:   pid.UserID,
		DeviceID: pid.DeviceID,
		Success:  true,
	})

	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatalf("EnsurePolling didn't unblock after response was sent")
	}

	// hitting EnsurePolling again should immediately return
	exp := ep.EnsurePolling(ctx, pid, "tokenHash")
	if exp {
		t.Fatalf("EnsurePolling said token was expired when it wasn't")
	}
	n.MustHaveNoSentPayloads(t)
}

func assertVal(t *testing.T, got, want interface{}) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("assertVal: got %v want %v", got, want)
	}
}
