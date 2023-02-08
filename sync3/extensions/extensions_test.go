package extensions

import (
	"context"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3/caches"
)

var (
	boolTrue = true
	roomA    = "!a:localhost"
	roomB    = "!b:localhost"
	ctx      = context.Background()
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
