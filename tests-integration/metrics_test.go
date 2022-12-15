package syncv3

import (
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func runMetricsServer(t *testing.T) *httptest.Server {
	return httptest.NewServer(promhttp.Handler())
}

func getMetrics(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+"/metrics", nil)
	if err != nil {
		t.Fatalf("failed to make metrics request: %s", err)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("failed to perform metrics request: %s", err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("/metrics returned HTTP %d", res.StatusCode)
	}
	defer res.Body.Close()
	blob, err := ioutil.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("/metrics failed to read response body: %s", err)
	}
	return strings.Split(string(blob), "\n")
}

func assertMetric(t *testing.T, lines []string, key string, val string) {
	t.Helper()
	for _, line := range lines {
		if !strings.HasPrefix(line, key+" ") {
			continue
		}
		segments := strings.Split(line, " ")
		if val != segments[1] {
			t.Errorf("want '%v %v' got '%v'", key, val, line)
		}
		return
	}
	t.Errorf("did not find key '%v' in %d lines", key, len(lines))
}

func TestMetricsNumPollers(t *testing.T) {
	metricKey := "sliding_sync_poller_num_pollers"
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString, true)
	defer v2.close()
	defer v3.close()
	metricsServer := runMetricsServer(t)
	defer metricsServer.Close()
	metrics := getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "0")
	// start a poller
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	v3.mustDoV3Request(t, aliceToken, sync3.Request{})
	// verify increase
	metrics = getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "1")
	// start another poller
	v2.addAccount(bob, bobToken)
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, bob, time.Now()),
			}),
		},
	})
	v3.mustDoV3Request(t, bobToken, sync3.Request{})
	// verify increase
	metrics = getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "2")
	// now invalidate a poller
	v2.invalidateToken(aliceToken)
	// verify decrease
	metrics = getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "1")
}

func TestMetricsNumConns(t *testing.T) {
	metricKey := "sliding_sync_api_num_active_conns"
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString, true)
	defer v2.close()
	defer v3.close()
	metricsServer := runMetricsServer(t)
	defer metricsServer.Close()
	metrics := getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "0")
	// start a poller
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	v3.mustDoV3Request(t, aliceToken, sync3.Request{})
	// verify increase
	metrics = getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "1")
	// start another poller
	v2.addAccount(bob, bobToken)
	v2.queueResponse(bob, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, bob, time.Now()),
			}),
		},
	})
	res := v3.mustDoV3Request(t, bobToken, sync3.Request{})
	// verify increase
	metrics = getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "2")

	// now live poll -> no increase
	res = v3.mustDoV3RequestWithPos(t, bobToken, res.Pos, sync3.Request{
		RoomSubscriptions: map[string]sync3.RoomSubscription{
			"!foo": { // doesn't matter, just so long as we return quickly
				TimelineLimit: 1,
			},
		},
	})
	metrics = getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "2")

	// now replace conn -> no increase
	v3.mustDoV3Request(t, aliceToken, sync3.Request{})
	metrics = getMetrics(t, metricsServer)
	assertMetric(t, metrics, metricKey, "2")

	// TODO: now expire conn -> decrease
}
