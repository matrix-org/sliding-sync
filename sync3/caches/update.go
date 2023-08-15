package caches

import (
	"fmt"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/state"
)

type Update interface {
	Type() string
}

type RoomUpdate interface {
	Update
	RoomID() string
	GlobalRoomMetadata() *internal.RoomMetadata
	UserRoomMetadata() *UserRoomData
}

// RoomEventUpdate corresponds to a single event seen in a joined room's timeline under sync v2.
type RoomEventUpdate struct {
	RoomUpdate
	EventData *EventData
}

func (u *RoomEventUpdate) Type() string {
	return fmt.Sprintf("RoomEventUpdate[%s]: %v", u.RoomID(), u.EventData.EventType)
}

// InviteUpdate corresponds to a key-value pair from a v2 sync's `invite` section.
type InviteUpdate struct {
	RoomUpdate
	InviteData InviteData
}

func (u *InviteUpdate) Type() string {
	return fmt.Sprintf("InviteUpdate[%s]", u.RoomID())
}

// TypingEdu corresponds to a typing EDU in the `ephemeral` section of a joined room's v2 sync resposne.
type TypingUpdate struct {
	RoomUpdate
}

func (u *TypingUpdate) Type() string {
	return fmt.Sprintf("TypingUpdate[%s]", u.RoomID())
}

// RecieptUpdate corresponds to a receipt EDU in the `ephemeral` section of a joined room's v2 sync resposne.
type ReceiptUpdate struct {
	RoomUpdate
	Receipt internal.Receipt
}

func (u *ReceiptUpdate) Type() string {
	return fmt.Sprintf("ReceiptUpdate[%s]", u.RoomID())
}

// UnreadCountUpdate represents a change in highlight or notification count change.
// The current counts are determinted from sync v2 responses; the pollers track
// changes to those counts to determine if they have decreased, remained unchanged,
// or increased.
type UnreadCountUpdate struct {
	RoomUpdate
	HasCountDecreased bool
}

func (u *UnreadCountUpdate) Type() string {
	return fmt.Sprintf("UnreadCountUpdate[%s]", u.RoomID())
}

// AccountDataUpdate represents the (global) `account_data` section of a v2 sync response.
type AccountDataUpdate struct {
	AccountData []state.AccountData
}

func (u *AccountDataUpdate) Type() string {
	return fmt.Sprintf("AccountDataUpdate len=%v", len(u.AccountData))
}

// RoomAccountDataUpdate represents the `account_data` section of joined room's v2 sync response.
type RoomAccountDataUpdate struct {
	RoomUpdate
	AccountData []state.AccountData
}

func (u *RoomAccountDataUpdate) Type() string {
	return fmt.Sprintf("RoomAccountDataUpdate[%s] len=%v", u.RoomID(), len(u.AccountData))
}

type DeviceDataUpdate struct {
	// no data; just wakes up the connection
	// data comes via sidechannels e.g the database
}

func (u DeviceDataUpdate) Type() string {
	return "DeviceDataUpdate"
}

type DeviceEventsUpdate struct {
	// no data; just wakes up the connection
	// data comes via sidechannels e.g the database
}

func (u DeviceEventsUpdate) Type() string {
	return "DeviceEventsUpdate"
}
