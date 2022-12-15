package caches

import (
	"encoding/json"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
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
}

type LeftRoomUpdate struct {
	RoomUpdate
}

type TypingUpdate struct {
	RoomUpdate
}

type ReceiptUpdate struct {
	RoomUpdate
	EphemeralEvent json.RawMessage
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
