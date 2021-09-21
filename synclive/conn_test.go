package synclive

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/internal"
)

// Test that Conn can send and receive arbitrary bytes based on positions
// The handler simply prefixes a string based on the request body and then increments with
// a basic counter. This ensures that we can modify the handler based on client data (which is
// required for ranges to work correctly). Note that 'positions' are entirely hidden from the handler.
func TestConn(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID:  "d",
		SessionID: "s",
	}
	lastPrefix := "a"
	pos := 100
	c := NewConn(connID, func(_ context.Context, _ ConnID, reqBody []byte) ([]byte, error) {
		if reqBody != nil {
			lastPrefix = string(reqBody)
		}
		pos += 1
		return []byte(fmt.Sprintf("%s-%d", lastPrefix, pos)), nil
	})

	// initial request
	nextPos, nextData, err := c.OnIncomingRequest(ctx, 0, []byte(`some_prefix`))
	assertPos(t, nextPos, 1)
	assertData(t, string(nextData), "some_prefix-101")
	assertNoError(t, err)
	// happy case, pos=1 and no prefix, so should use the last one
	nextPos, nextData, err = c.OnIncomingRequest(ctx, 1, nil)
	assertPos(t, nextPos, 2)
	assertData(t, string(nextData), "some_prefix-102")
	assertNoError(t, err)
	// updates work too
	nextPos, nextData, err = c.OnIncomingRequest(ctx, 2, []byte(`more`))
	assertPos(t, nextPos, 3)
	assertData(t, string(nextData), "more-103")
	assertNoError(t, err)
	// bogus position returns a 400
	_, _, err = c.OnIncomingRequest(ctx, 31415, []byte(`more`))
	if err == nil {
		t.Fatalf("expected error, got none")
	}
	if err.StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", err.StatusCode)
	}
}

// Test that Conn is blocking and linearises requests to OnIncomingRequest
// It does this by triggering 2 OnIncomingRequest calls one after the other with a 1ms delay
// The first request will "process" for 10ms whereas the 2nd request will process immediately.
// If we don't block, we'll get "hi2" then "hi". If we block, we should see "hi" then "hi2".
func TestConnBlocking(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID:  "d",
		SessionID: "s",
	}
	ch := make(chan string)
	c := NewConn(connID, func(_ context.Context, _ ConnID, reqBody []byte) ([]byte, error) {
		if string(reqBody) == "hi" {
			time.Sleep(10 * time.Millisecond)
		}
		ch <- string(reqBody)
		return reqBody, nil // echo bot
	})

	// two connection call the incoming request function at the same time, they should get queued up
	// and processed in series.
	// this should block until we read from ch
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.OnIncomingRequest(ctx, 0, []byte(`hi`))
	}()
	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Millisecond) // this req happens 2nd
		c.OnIncomingRequest(ctx, 0, []byte(`hi2`))
	}()
	go func() {
		wg.Wait()
		close(ch)
	}()
	want := []string{"hi", "hi2"}
	for resp := range ch {
		if resp != want[0] {
			t.Fatalf("got %v want %v", resp, want[0])
		}
		want = want[1:]
	}

}

func TestConnRetries(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID:  "d",
		SessionID: "s",
	}
	callCount := 0
	c := NewConn(connID, func(_ context.Context, _ ConnID, _ []byte) ([]byte, error) {
		callCount += 1
		return []byte(`yep`), nil
	})
	nextPos, nextData, err := c.OnIncomingRequest(ctx, 0, nil)
	assertPos(t, nextPos, 1)
	assertData(t, string(nextData), "yep")
	assertNoError(t, err)
	if callCount != 1 {
		t.Fatalf("wanted HandleIncomingRequest called 1 times, got %d", callCount)
	}
	nextPos, nextData, err = c.OnIncomingRequest(ctx, 1, nil)
	assertPos(t, nextPos, 2)
	assertData(t, string(nextData), "yep")
	assertNoError(t, err)
	if callCount != 2 {
		t.Fatalf("wanted HandleIncomingRequest called 2 times, got %d", callCount)
	}
	// retry! Shouldn't invoke handler again
	nextPos, nextData, err = c.OnIncomingRequest(ctx, 1, nil)
	assertPos(t, nextPos, 2)
	assertData(t, string(nextData), "yep")
	assertNoError(t, err)
	if callCount != 2 {
		t.Fatalf("wanted HandleIncomingRequest called 2 times, got %d", callCount)
	}
	// retry! but with modified request body, so should invoke handler again
	nextPos, nextData, err = c.OnIncomingRequest(ctx, 1, []byte(`data`))
	assertPos(t, nextPos, 3)
	assertData(t, string(nextData), "yep")
	assertNoError(t, err)
	if callCount != 3 {
		t.Fatalf("wanted HandleIncomingRequest called 3 times, got %d", callCount)
	}
}

func TestConnErrors(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID:  "d",
		SessionID: "s",
	}
	errCh := make(chan error, 1)
	c := NewConn(connID, func(_ context.Context, _ ConnID, _ []byte) ([]byte, error) {
		return nil, <-errCh
	})

	// random errors = 500
	errCh <- errors.New("oops")
	_, _, herr := c.OnIncomingRequest(ctx, 0, nil)
	if herr.StatusCode != 500 {
		t.Fatalf("random errors should be status 500, got %d", herr.StatusCode)
	}
	errCh <- &internal.HandlerError{
		StatusCode: 400,
		Err:        errors.New("no way!"),
	}
	_, _, herr = c.OnIncomingRequest(ctx, 0, nil)
	if herr.StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", herr.StatusCode)
	}
}

func assertPos(t *testing.T, nextPos, wantPos int64) {
	t.Helper()
	if nextPos != wantPos {
		t.Errorf("got pos %d want pos %d", nextPos, wantPos)
	}
}

func assertData(t *testing.T, nextData, wantData string) {
	t.Helper()
	if nextData != wantData {
		t.Errorf("got data %v want data %v", nextData, wantData)
	}
}

func assertNoError(t *testing.T, err *internal.HandlerError) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error: %v", err)
	}
}
