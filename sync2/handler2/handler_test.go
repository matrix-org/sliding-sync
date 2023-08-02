package handler2_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"

	"github.com/matrix-org/sliding-sync/pubsub"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync2/handler2"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/rs/zerolog"
)

var postgresURI string

func TestMain(m *testing.M) {
	postgresURI = testutils.PrepareDBConnectionString()
	exitCode := m.Run()
	os.Exit(exitCode)
}

type pollInfo struct {
	pid         sync2.PollerID
	accessToken string
	v2since     string
	isStartup   bool
}

type mockPollerMap struct {
	calls []pollInfo
}

func (p *mockPollerMap) NumPollers() int {
	return 0
}
func (p *mockPollerMap) Terminate() {}

func (p *mockPollerMap) DeviceIDs(userID string) []string {
	return nil
}

func (p *mockPollerMap) EnsurePolling(pid sync2.PollerID, accessToken, v2since string, isStartup bool, logger zerolog.Logger) {
	p.calls = append(p.calls, pollInfo{
		pid:         pid,
		accessToken: accessToken,
		v2since:     v2since,
		isStartup:   isStartup,
	})
}

func (p *mockPollerMap) assertCallExists(t *testing.T, pi pollInfo) {
	for _, c := range p.calls {
		if reflect.DeepEqual(pi, c) {
			return
		}
	}
	t.Fatalf("assertCallExists: did not find %+v", pi)
}

type mockPub struct {
	calls   []pubsub.Payload
	mu      *sync.Mutex
	waiters map[string][]chan struct{}
}

func newMockPub() *mockPub {
	return &mockPub{
		mu:      &sync.Mutex{},
		waiters: make(map[string][]chan struct{}),
	}
}

// Notify chanName that there is a new payload p. Return an error if we failed to send the notification.
func (p *mockPub) Notify(chanName string, payload pubsub.Payload) error {
	p.calls = append(p.calls, payload)
	p.mu.Lock()
	for _, ch := range p.waiters[payload.Type()] {
		close(ch)
	}
	p.waiters[payload.Type()] = nil // don't re-notify for 2nd+ payload
	p.mu.Unlock()
	return nil
}

func (p *mockPub) WaitForPayloadType(t string) chan struct{} {
	ch := make(chan struct{})
	p.mu.Lock()
	p.waiters[t] = append(p.waiters[t], ch)
	p.mu.Unlock()
	return ch
}

func (p *mockPub) DoWait(t *testing.T, errMsg string, ch chan struct{}, wantTimeOut bool) {
	select {
	case <-ch:
		if wantTimeOut {
			t.Fatalf("expected to timeout, but received on channel")
		}
		return
	case <-time.After(time.Second):
		if !wantTimeOut {
			t.Fatalf("DoWait: timed out waiting: %s", errMsg)
		}
	}
}

// Close is called when we should stop listening.
func (p *mockPub) Close() error { return nil }

type mockSub struct{}

// Begin listening on this channel with this callback starting from this position. Blocks until Close() is called.
func (s *mockSub) Listen(chanName string, fn func(p pubsub.Payload)) error { return nil }

// Close the listener. No more callbacks should fire.
func (s *mockSub) Close() error { return nil }

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	t.Fatalf("assertNoError: %v", err)
}

// Test that if you call EnsurePolling you get back V2InitialSyncComplete down pubsub and the poller
// map is called correctly
func TestHandlerFreshEnsurePolling(t *testing.T) {
	store := state.NewStorage(postgresURI)
	v2Store := sync2.NewStore(postgresURI, "secret")
	pMap := &mockPollerMap{}
	pub := newMockPub()
	sub := &mockSub{}
	h, err := handler2.NewHandler(pMap, v2Store, store, pub, sub, false, time.Minute)
	assertNoError(t, err)
	alice := "@alice:localhost"
	deviceID := "ALICE"
	token := "aliceToken"

	var tok *sync2.Token
	sqlutil.WithTransaction(v2Store.DB, func(txn *sqlx.Tx) error {
		// the device and token needs to already exist prior to EnsurePolling
		err = v2Store.DevicesTable.InsertDevice(txn, alice, deviceID)
		assertNoError(t, err)
		tok, err = v2Store.TokensTable.Insert(txn, token, alice, deviceID, time.Now())
		assertNoError(t, err)
		return nil
	})

	payloadInitialSyncComplete := pubsub.V2InitialSyncComplete{
		UserID:   alice,
		DeviceID: deviceID,
	}
	ch := pub.WaitForPayloadType(payloadInitialSyncComplete.Type())
	// ask the handler to start polling
	h.EnsurePolling(&pubsub.V3EnsurePolling{
		UserID:          alice,
		DeviceID:        deviceID,
		AccessTokenHash: tok.AccessTokenHash,
	})
	pub.DoWait(t, "didn't see V2InitialSyncComplete", ch, false)

	// make sure we polled with the token i.e it did a db hit
	pMap.assertCallExists(t, pollInfo{
		pid: sync2.PollerID{
			UserID:   alice,
			DeviceID: deviceID,
		},
		accessToken: token,
		v2since:     "",
		isStartup:   false,
	})

}

func TestSetTypingConcurrently(t *testing.T) {
	store := state.NewStorage(postgresURI)
	v2Store := sync2.NewStore(postgresURI, "secret")
	pMap := &mockPollerMap{}
	pub := newMockPub()
	sub := &mockSub{}
	h, err := handler2.NewHandler(pMap, v2Store, store, pub, sub, false, time.Minute)
	assertNoError(t, err)
	ctx := context.Background()

	roomID := "!typing:localhost"

	typingType := pubsub.V2Typing{}

	// startSignal is used to synchronize calling SetTyping
	startSignal := make(chan struct{})
	// Call SetTyping twice, this may happen with pollers for the same user
	go func() {
		<-startSignal
		h.SetTyping(ctx, sync2.PollerID{UserID: "@alice", DeviceID: "aliceDevice"}, roomID, json.RawMessage(`{"content":{"user_ids":["@alice:localhost"]}}`))
	}()
	go func() {
		<-startSignal
		h.SetTyping(ctx, sync2.PollerID{UserID: "@bob", DeviceID: "bobDevice"}, roomID, json.RawMessage(`{"content":{"user_ids":["@alice:localhost"]}}`))
	}()

	close(startSignal)

	// Wait for the event to be published
	ch := pub.WaitForPayloadType(typingType.Type())
	pub.DoWait(t, "didn't see V2Typing", ch, false)
	ch = pub.WaitForPayloadType(typingType.Type())
	// Wait again, but this time we expect to timeout.
	pub.DoWait(t, "saw unexpected V2Typing", ch, true)

	// We expect only one call to Notify, as the hashes should match
	if gotCalls := len(pub.calls); gotCalls != 1 {
		t.Fatalf("expected only one call to notify, got %d", gotCalls)
	}
}
