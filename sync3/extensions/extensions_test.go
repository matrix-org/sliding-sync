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
					Core: Core{
						Enabled: &boolTrue,
					},
				},
			},
			next: &Request{
				AccountData: &AccountDataRequest{
					Core: Core{
						Enabled: &boolFalse,
					},
				},
			},
			want: &Request{
				AccountData: &AccountDataRequest{
					Core: Core{
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
					Core: Core{
						Enabled: &boolTrue,
					},
				},
			},
			want: &Request{
				AccountData: &AccountDataRequest{
					Core: Core{
						Enabled: &boolTrue,
						Lists:   []string{"*"},
						Rooms:   []string{"*"},
					},
				},
			},
		},
		{
			name: "enabled: true to <nil>", // sticky extension
			curr: &Request{
				AccountData: &AccountDataRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
			},
			next: &Request{},
			want: &Request{
				AccountData: &AccountDataRequest{
					Core: Core{
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
					Core: Core{
						Enabled: &boolFalse,
					},
				},
				E2EE: &E2EERequest{
					Core: Core{
						Enabled: &boolFalse,
					},
				},
				Receipts: &ReceiptsRequest{
					Core: Core{
						Enabled: &boolFalse,
					},
				},
				Typing: &TypingRequest{
					Core: Core{
						Enabled: &boolFalse,
					},
				},
				ToDevice: &ToDeviceRequest{
					Core: Core{
						Enabled: &boolFalse,
					},
					Limit: 42,
				},
			},
			next: &Request{
				AccountData: &AccountDataRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				E2EE: &E2EERequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				Receipts: &ReceiptsRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				Typing: &TypingRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				ToDevice: &ToDeviceRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
					Since: "A",
				},
			},
			want: &Request{
				AccountData: &AccountDataRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				E2EE: &E2EERequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				Receipts: &ReceiptsRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				Typing: &TypingRequest{
					Core: Core{
						Enabled: &boolTrue,
					},
				},
				ToDevice: &ToDeviceRequest{
					Core: Core{
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
