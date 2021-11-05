package sync3

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/internal"
)

type connHandlerMock struct {
	fn func(ctx context.Context, cid ConnID, req *Request) (*Response, error)
}

func (c *connHandlerMock) OnIncomingRequest(ctx context.Context, cid ConnID, req *Request) (*Response, error) {
	return c.fn(ctx, cid, req)
}
func (c *connHandlerMock) UserID() string {
	return "dummy"
}
func (c *connHandlerMock) Destroy() {}

// Test that Conn can send and receive requests based on positions
func TestConn(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID:  "d",
		SessionID: "s",
	}
	count := int64(100)
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request) (*Response, error) {
		count += 1
		return &Response{
			Count: count,
		}, nil
	}})

	// initial request
	resp, err := c.OnIncomingRequest(ctx, &Request{
		pos: 0,
	})
	assertInt(t, resp.Pos, 1)
	assertInt(t, resp.Count, 101)
	assertNoError(t, err)
	// happy case, pos=1
	resp, err = c.OnIncomingRequest(ctx, &Request{
		pos: 1,
	})
	assertInt(t, resp.Pos, 2)
	assertInt(t, resp.Count, 102)
	assertNoError(t, err)
	// bogus position returns a 400
	_, err = c.OnIncomingRequest(ctx, &Request{
		pos: 31415,
	})
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
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request) (*Response, error) {
		if req.Sort[0] == "hi" {
			time.Sleep(10 * time.Millisecond)
		}
		ch <- req.Sort[0]
		return &Response{}, nil
	}})

	// two connection call the incoming request function at the same time, they should get queued up
	// and processed in series.
	// this should block until we read from ch
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		c.OnIncomingRequest(ctx, &Request{
			Sort: []string{"hi"},
		})
	}()
	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Millisecond) // this req happens 2nd
		c.OnIncomingRequest(ctx, &Request{
			Sort: []string{"hi2"},
		})
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
	callCount := int64(0)
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request) (*Response, error) {
		callCount += 1
		return &Response{Count: 20}, nil
	}})
	resp, err := c.OnIncomingRequest(ctx, &Request{})
	assertInt(t, resp.Pos, 1)
	assertInt(t, resp.Count, 20)
	assertInt(t, callCount, 1)
	assertNoError(t, err)
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 1})
	assertInt(t, resp.Pos, 2)
	assertInt(t, resp.Count, 20)
	assertInt(t, callCount, 2)
	assertNoError(t, err)
	// retry! Shouldn't invoke handler again
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 1})
	assertInt(t, resp.Pos, 2)
	assertInt(t, resp.Count, 20)
	assertInt(t, callCount, 2) // this doesn't increment
	assertNoError(t, err)
	// retry! but with modified request body, so should invoke handler again
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 1, Sort: []string{"by_name"}})
	assertInt(t, resp.Pos, 3)
	assertInt(t, resp.Count, 20)
	assertInt(t, callCount, 3)
	assertNoError(t, err)
}

func TestConnErrors(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID:  "d",
		SessionID: "s",
	}
	errCh := make(chan error, 1)
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request) (*Response, error) {
		return nil, <-errCh
	}})

	// random errors = 500
	errCh <- errors.New("oops")
	_, herr := c.OnIncomingRequest(ctx, &Request{})
	if herr.StatusCode != 500 {
		t.Fatalf("random errors should be status 500, got %d", herr.StatusCode)
	}
	// explicit error codes should be passed through
	errCh <- &internal.HandlerError{
		StatusCode: 400,
		Err:        errors.New("no way!"),
	}
	_, herr = c.OnIncomingRequest(ctx, &Request{})
	if herr.StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", herr.StatusCode)
	}
}

func TestConnErrorsNoCache(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID:  "d",
		SessionID: "s",
	}
	errCh := make(chan error, 1)
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request) (*Response, error) {
		select {
		case e := <-errCh:
			return nil, e
		default:
			return &Response{}, nil
		}
	}})
	// errors should not be cached
	resp, herr := c.OnIncomingRequest(ctx, &Request{})
	if herr != nil {
		t.Fatalf("expected no error, got %+v", herr)
	}
	// now this returns a temporary error
	errCh <- &internal.HandlerError{
		StatusCode: 400,
		Err:        errors.New("no way!"),
	}
	_, herr = c.OnIncomingRequest(ctx, &Request{pos: resp.Pos})
	if herr.StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", herr.StatusCode)
	}
	// but doing the exact same request should now work
	_, herr = c.OnIncomingRequest(ctx, &Request{pos: resp.Pos})
	if herr != nil {
		t.Fatalf("expected no error, got %+v", herr)
	}
}

func assertInt(t *testing.T, nextPos, wantPos int64) {
	t.Helper()
	if nextPos != wantPos {
		t.Errorf("got %d pos %d", nextPos, wantPos)
	}
}

func assertNoError(t *testing.T, err *internal.HandlerError) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error: %v", err)
	}
}
