package syncv3_test

import (
	"context"
	"errors"
	"github.com/matrix-org/complement/client"
	"github.com/matrix-org/sliding-sync/sync3"
	"net/http"
	"net/url"
	"testing"
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

	client.WithQueries(url.Values{
		"timeout": []string{"1000"},
		"pos":     []string{aliceRes.Pos},
	})(req)
	client.WithRawBody([]byte("{}"))
	client.WithContentType("application/json")(req)
	req.Header.Set("Authorization", "Bearer "+alice.AccessToken)

	done := make(chan struct{})

	go func() {
		t.Log("Alice makes her second sync.")
		t.Log(req)
		_, err := alice.Client.Do(req)
		if err != nil {
			t.Error(err)
		}

		done <- struct{}{}
	}()

	t.Log("Alice logs out.")
	alice.MustDo(t, "POST", []string{"_matrix", "client", "v3", "logout"})

	t.Log("Alice waits for her second sync response.")
	<-done

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Logf("ctx.Err(): got %v, expected %v", ctx.Err(), context.Canceled)
	}
}
