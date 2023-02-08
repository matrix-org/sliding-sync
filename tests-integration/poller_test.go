package syncv3

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/matrix-org/sliding-sync/sync2"
	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/matrix-org/sliding-sync/testutils"
	"github.com/matrix-org/sliding-sync/testutils/m"
	"github.com/tidwall/gjson"
)

// Tests that if Alice is syncing with Device A, then begins syncing on a new Device B, we use
// a custom filter on the first sync to just pull out to-device events (which is faster)
func TestSecondPollerFiltersToDevice(t *testing.T) {
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	deviceAToken := "DEVICE_A_TOKEN"
	v2.addAccount(alice, deviceAToken)
	v2.queueResponse(deviceAToken, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(roomEvents{
				roomID: "!unimportant",
				events: createRoomState(t, alice, time.Now()),
			}),
		},
	})
	// seed the proxy with data and get the first poller running
	v3.mustDoV3Request(t, deviceAToken, sync3.Request{})

	// now sync with device B, and check we send the filter up
	deviceBToken := "DEVICE_B_TOKEN"
	v2.addAccount(alice, deviceBToken)
	seenInitialRequest := false
	v2.CheckRequest = func(userID, token string, req *http.Request) {
		if userID != alice || token != deviceBToken {
			return
		}
		qps := req.URL.Query()
		since := qps.Get("since")
		filter := qps.Get("filter")
		t.Logf("CheckRequest: %v %v since=%v filter=%v", userID, token, since, filter)
		if since != "" {
			// there should be no filter
			if filter != "" {
				t.Errorf("expected no filter when ?since= is supplied, got %v", filter)
			}
			return
		}
		if filter == "" {
			t.Errorf("missing ?filter= on initial request")
			return
		}

		filterJSON := gjson.Parse(filter)
		roomsFilter := filterJSON.Get("room.rooms")
		if !roomsFilter.Exists() {
			t.Errorf("missing room.rooms in filter: %s", filter)
		}
		if len(roomsFilter.Array()) != 0 {
			t.Errorf("room.rooms array is >0 : %s", filter)
		}
		seenInitialRequest = true
	}

	wantMsg := json.RawMessage(`{"type":"f","content":{"f":"b"}}`)
	v2.queueResponse(deviceBToken, sync2.SyncResponse{
		NextBatch: "a",
		ToDevice: sync2.EventsResponse{
			Events: []json.RawMessage{
				wantMsg,
			},
		},
	})
	boolTrue := true
	res := v3.mustDoV3Request(t, deviceBToken, sync3.Request{
		Extensions: extensions.Request{
			ToDevice: &extensions.ToDeviceRequest{
				Enableable: extensions.Enableable{Enabled: &boolTrue},
			},
		},
	})

	if !seenInitialRequest {
		t.Fatalf("did not see initial request for 2nd device")
	}
	// the first request will not wait for the response before returning due to device A. Poll again
	// and now we should see the to-device msg.
	res = v3.mustDoV3RequestWithPos(t, deviceBToken, res.Pos, sync3.Request{})
	m.MatchResponse(t, res, m.MatchToDeviceMessages([]json.RawMessage{wantMsg}))
}
