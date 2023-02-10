package extensions

import (
	"context"
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

var (
	boolTrue  = true
	boolFalse = false
	roomA     = "!a:localhost"
	roomB     = "!b:localhost"
	roomC     = "!c:localhost"
	ctx       = context.Background()
)

type dummyRoomUpdate struct {
	roomID         string
	userRoomData   *caches.UserRoomData
	globalMetadata *internal.RoomMetadata
}

func (u *dummyRoomUpdate) Type() string {
	return "dummy"
}
func (u *dummyRoomUpdate) RoomID() string {
	return u.roomID
}
func (u *dummyRoomUpdate) GlobalRoomMetadata() *internal.RoomMetadata {
	return u.globalMetadata
}
func (u *dummyRoomUpdate) UserRoomMetadata() *caches.UserRoomData {
	return u.userRoomData
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	t.Fatalf(err.Error())
}

func TestExtension_ApplyDelta(t *testing.T) {
	testCases := []struct {
		name string
		curr *Request
		next *Request
		want *Request
	}{
		{
			name: "enabled: true to enabled: false", // updating extension
			curr: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
			},
			next: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolFalse,
					},
				},
			},
			want: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolFalse,
					},
				},
			},
		},
		{
			name: "<nil> to enabled: true", // late-enabling extension
			curr: &Request{},
			next: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
			},
			want: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
			},
		},
		{
			name: "enabled: true to <nil>", // sticky extension
			curr: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
			},
			next: &Request{},
			want: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
			},
		},
		{
			name: "<nil> to <nil>", // no extensions
			curr: &Request{},
			next: &Request{},
			want: &Request{},
		},
		{
			name: "all extensions", // makes sure all extension fields are correct
			curr: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolFalse,
					},
				},
				E2EE: &E2EERequest{
					Enableable: Enableable{
						Enabled: &boolFalse,
					},
				},
				Receipts: &ReceiptsRequest{
					Enableable: Enableable{
						Enabled: &boolFalse,
					},
				},
				Typing: &TypingRequest{
					Enableable: Enableable{
						Enabled: &boolFalse,
					},
				},
				ToDevice: &ToDeviceRequest{
					Enableable: Enableable{
						Enabled: &boolFalse,
					},
					Limit: 42,
				},
			},
			next: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				E2EE: &E2EERequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				Receipts: &ReceiptsRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				Typing: &TypingRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				ToDevice: &ToDeviceRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
					Since: "A",
				},
			},
			want: &Request{
				AccountData: &AccountDataRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				E2EE: &E2EERequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				Receipts: &ReceiptsRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				Typing: &TypingRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
				},
				ToDevice: &ToDeviceRequest{
					Enableable: Enableable{
						Enabled: &boolTrue,
					},
					Since: "A",
					Limit: 42,
				},
			},
		},
	}
	for _, tc := range testCases {
		got := tc.curr.ApplyDelta(tc.next)
		if !reflect.DeepEqual(&got, tc.want) {
			t.Errorf("ApplyDelta: %s got %+v want %+v", tc.name, &got, tc.want)
		}
	}
}
