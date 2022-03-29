package caches

import (
	"github.com/matrix-org/sync-v3/internal"
	"github.com/matrix-org/sync-v3/state"
)

type Update interface {
}

type RoomUpdate interface {
	Update
	RoomID() string
	GlobalRoomMetadata() *internal.RoomMetadata
	UserRoomMetadata() *UserRoomData
}

type RoomEventUpdate struct {
	RoomUpdate
	EventData *EventData
}

type InviteUpdate struct {
	RoomUpdate
	InviteData InviteData
	Retired    bool
}

type UnreadCountUpdate struct {
	RoomUpdate
	HasCountDecreased bool
}

type AccountDataUpdate struct {
	AccountData []state.AccountData
}

type RoomAccountDataUpdate struct {
	RoomUpdate
	AccountData []state.AccountData
}

// Alerts result in changes to ops, subs or ext modifications
// Alerts can update internal conn state
// Dispatcher thread ultimately fires alerts OR poller thread e.g OnUnreadCounts
