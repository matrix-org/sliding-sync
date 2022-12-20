package sync3

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/internal"
)

type connHandlerMock struct {
	fn func(ctx context.Context, cid ConnID, req *Request, isInitial bool) (*Response, error)
}

func (c *connHandlerMock) OnIncomingRequest(ctx context.Context, cid ConnID, req *Request, init bool) (*Response, error) {
	return c.fn(ctx, cid, req, init)
}
func (c *connHandlerMock) UserID() string {
	return "dummy"
}
func (c *connHandlerMock) Destroy()    {}
func (c *connHandlerMock) Alive() bool { return true }

// Test that Conn can send and receive requests based on positions
func TestConn(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID: "d",
	}
	count := 100
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request, isInitial bool) (*Response, error) {
		count += 1
		return &Response{
			Lists: map[string]ResponseList{
				"a": {
					Count: count,
				},
			},
		}, nil
	}})

	// initial request
	resp, err := c.OnIncomingRequest(ctx, &Request{
		pos: 0,
	})
	assertNoError(t, err)
	assertPos(t, resp.Pos, 1)
	assertInt(t, resp.Lists["a"].Count, 101)

	// happy case, pos=1
	resp, err = c.OnIncomingRequest(ctx, &Request{
		pos: 1,
	})
	assertPos(t, resp.Pos, 2)
	assertInt(t, resp.Lists["a"].Count, 102)
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
		DeviceID: "d",
	}
	ch := make(chan string)
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request, init bool) (*Response, error) {
		if req.Lists["a"].Sort[0] == "hi" {
			time.Sleep(10 * time.Millisecond)
		}
		ch <- req.Lists["a"].Sort[0]
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
			Lists: map[string]RequestList{
				"a": {
					Sort: []string{"hi"},
				},
			},
		})
	}()
	go func() {
		defer wg.Done()
		time.Sleep(1 * time.Millisecond) // this req happens 2nd
		c.OnIncomingRequest(ctx, &Request{
			Lists: map[string]RequestList{
				"a": {
					Sort: []string{"hi2"},
				},
			},
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
		DeviceID: "d",
	}
	callCount := 0
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request, init bool) (*Response, error) {
		callCount += 1
		return &Response{Lists: map[string]ResponseList{
			"a": {
				Count: 20,
			},
		}}, nil
	}})
	resp, err := c.OnIncomingRequest(ctx, &Request{})
	assertPos(t, resp.Pos, 1)
	assertInt(t, resp.Lists["a"].Count, 20)
	assertInt(t, callCount, 1)
	assertNoError(t, err)
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 1})
	assertPos(t, resp.Pos, 2)
	assertInt(t, resp.Lists["a"].Count, 20)
	assertInt(t, callCount, 2)
	assertNoError(t, err)
	// retry! Shouldn't invoke handler again
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 1})
	assertPos(t, resp.Pos, 2)
	assertInt(t, resp.Lists["a"].Count, 20)
	assertInt(t, callCount, 2) // this doesn't increment
	assertNoError(t, err)
	// retry! but with modified request body, so should invoke handler again but return older data (buffered)
	resp, err = c.OnIncomingRequest(ctx, &Request{
		pos: 1, Lists: map[string]RequestList{
			"a": {
				Sort: []string{SortByName},
			},
		}})
	assertPos(t, resp.Pos, 2)
	assertInt(t, resp.Lists["a"].Count, 20)
	assertInt(t, callCount, 3) // this doesn't increment
	assertNoError(t, err)
}

func TestConnBufferRes(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID: "d",
	}
	callCount := 0
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request, init bool) (*Response, error) {
		callCount += 1
		return &Response{Lists: map[string]ResponseList{
			"a": {
				Count: callCount,
			},
		}}, nil
	}})
	resp, err := c.OnIncomingRequest(ctx, &Request{})
	assertNoError(t, err)
	assertPos(t, resp.Pos, 1)
	assertInt(t, resp.Lists["a"].Count, 1)
	assertInt(t, callCount, 1)
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 1})
	assertNoError(t, err)
	assertPos(t, resp.Pos, 2)
	assertInt(t, resp.Lists["a"].Count, 2)
	assertInt(t, callCount, 2)
	// retry with modified request data, should invoke handler again!
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 1, TxnID: "a"})
	assertNoError(t, err)
	assertPos(t, resp.Pos, 2)
	assertInt(t, resp.Lists["a"].Count, 2)
	assertInt(t, callCount, 3) // this DOES increment, the response is buffered and not returned yet.
	// retry with same request body, so should NOT invoke handler again and return buffered response
	resp, err = c.OnIncomingRequest(ctx, &Request{pos: 2, TxnID: "a"})
	assertNoError(t, err)
	assertPos(t, resp.Pos, 3)
	assertInt(t, resp.Lists["a"].Count, 3)
	assertInt(t, callCount, 3)
}

func TestConnErrors(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID: "d",
	}
	errCh := make(chan error, 1)
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request, init bool) (*Response, error) {
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
		DeviceID: "d",
	}
	errCh := make(chan error, 1)
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request, init bool) (*Response, error) {
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
	_, herr = c.OnIncomingRequest(ctx, &Request{pos: resp.PosInt()})
	if herr.StatusCode != 400 {
		t.Fatalf("expected status 400, got %d", herr.StatusCode)
	}
	// but doing the exact same request should now work
	_, herr = c.OnIncomingRequest(ctx, &Request{pos: resp.PosInt()})
	if herr != nil {
		t.Fatalf("expected no error, got %+v", herr)
	}
}

// Regression test to ensure that the Conn remembers which responses it has sent to the client, rather
// than just the latest (highest pos) response. We test this by creating a long buffer of outstanding responses,
// then ack a few of them, then do a retry which will fail as the pos the retry represents is unknown.
func TestConnBufferRememberInflight(t *testing.T) {
	ctx := context.Background()
	connID := ConnID{
		DeviceID: "d",
	}
	callCount := 0
	c := NewConn(connID, &connHandlerMock{func(ctx context.Context, cid ConnID, req *Request, init bool) (*Response, error) {
		callCount += 1
		return &Response{Lists: map[string]ResponseList{
			"a": {
				Count: callCount,
			},
		}}, nil
	}})
	steps := []struct {
		req           *Request
		wantErr       bool
		wantResPos    int
		wantResCount  int
		wantCallCount int
	}{
		{ // initial request
			req:           &Request{},
			wantResPos:    1,
			wantResCount:  1,
			wantCallCount: 1,
		},
		{ // happy path, keep syncing
			req:           &Request{pos: 1},
			wantResPos:    2,
			wantResCount:  2,
			wantCallCount: 2,
		},
		{ // lost HTTP response, cancelled request, new params, returns buffered response but does execute handler
			req:           &Request{pos: 1, TxnID: "a"},
			wantResPos:    2,
			wantResCount:  2,
			wantCallCount: 3,
		},
		{ // lost HTTP response, cancelled request, new params, returns same buffered response but does execute handler again
			req:           &Request{pos: 1, TxnID: "b"},
			wantResPos:    2,
			wantResCount:  2,
			wantCallCount: 4,
		},
		{ // lost HTTP response, cancelled request, new params, returns same buffered response but does execute handler again
			req:           &Request{pos: 1, TxnID: "c"},
			wantResPos:    2,
			wantResCount:  2,
			wantCallCount: 5,
		},
		{ // response returned, params same now, begin consuming buffer..
			req:           &Request{pos: 2, TxnID: "c"},
			wantResPos:    3,
			wantResCount:  3,
			wantCallCount: 5,
		},
		{ // response returned, params same now, begin consuming buffer..
			req:           &Request{pos: 3, TxnID: "c"},
			wantResPos:    4,
			wantResCount:  4,
			wantCallCount: 5,
		},
		{ // response returned, params same now, begin consuming buffer..
			req:           &Request{pos: 4, TxnID: "c"},
			wantResPos:    5,
			wantResCount:  5,
			wantCallCount: 5,
		},
		{ // response lost, should send buffered response if it isn't just doing the latest one (which is pos=5)
			req:           &Request{pos: 4, TxnID: "c"},
			wantResPos:    5,
			wantResCount:  5,
			wantCallCount: 5,
		},
	}
	var resp *Response
	var err *internal.HandlerError
	for i, step := range steps {
		t.Logf("Executing step %d", i)
		resp, err = c.OnIncomingRequest(ctx, step.req)
		if !step.wantErr {
			assertNoError(t, err)
		}
		assertPos(t, resp.Pos, step.wantResPos)
		assertInt(t, resp.Lists["a"].Count, step.wantResCount)
		assertInt(t, callCount, step.wantCallCount)
	}
}

func assertPos(t *testing.T, pos string, wantPos int) {
	t.Helper()
	gotPos, err := strconv.Atoi(pos)
	if err != nil {
		t.Errorf("pos isn't an int: %s", err)
		return
	}
	assertInt(t, int(gotPos), wantPos)
}

func assertInt(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("got %d want %d", got, want)
	}
}

func assertNoError(t *testing.T, err *internal.HandlerError) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error: %v", err)
	}
}
