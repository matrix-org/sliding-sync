package syncv3_test

import (
	"context"
	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestRequestCancelledWhenItsConnIsDestroyed(t *testing.T) {
	alice := registerNamedUser(t, "alice")

	t.Log("Alice does an initial sliding sync.")
	aliceRes := alice.SlidingSync(t, sync3.Request{})

	t.Log("Alice prepares a second sliding sync request.")
	ctx := context.Background()
	req, err := http.NewRequestWithContext(ctx, "POST", proxyBaseURL+"/_matrix/client/unstable/org.matrix.msc3575/sync", nil)
	if err != nil {
		t.Fatal(err)
	}

	timeout := time.Second
	cancelWithin := 100 * time.Millisecond

	client.WithQueries(url.Values{
		"timeout": []string{strconv.FormatInt(timeout.Milliseconds(), 10)},
		"pos":     []string{aliceRes.Pos},
	})(req)
	client.WithRawBody([]byte("{}"))
	client.WithContentType("application/json")(req)
	req.Header.Set("Authorization", "Bearer "+alice.AccessToken)

	type secondSyncResponse struct {
		res      *http.Response
		body     gjson.Result
		duration time.Duration
	}
	done := make(chan secondSyncResponse)

	go func() {
		t.Log("Alice makes her second sync.")
		t.Log(req)
		start := time.Now()
		res, err := alice.Client.Do(req)
		end := time.Now()

		if err != nil {
			t.Error(err)
		}

		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Error(err)
		}

		done <- secondSyncResponse{
			res:      res,
			body:     gjson.ParseBytes(body),
			duration: end.Sub(start),
		}

	}()

	t.Log("Alice logs out.")
	alice.MustDo(t, "POST", []string{"_matrix", "client", "v3", "logout"})

	t.Log("Alice waits for her second sync to return.")
	response := <-done

	// TODO: At first I expected that this the cancelled request should return 400 M_UNKNOWN_POS.
	// But I think that is best handled on the next incoming request.
	// assertEqual(t, "status code", response.res.StatusCode, http.StatusBadRequest)
	// assertEqual(t, "response errcode", response.body.Get("errcode").Str, "M_UNKNOWN_POS")

	if response.duration > cancelWithin {
		t.Errorf("Waited for %s, but expected second sync to cancel after at most %s", response.duration, cancelWithin)
	}
}
