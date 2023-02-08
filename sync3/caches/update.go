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

type RoomEventUpdate struct {
	RoomUpdate
	EventData *EventData
}

func (u *RoomEventUpdate) Type() string {
	return fmt.Sprintf("RoomEventUpdate[%s]: %v", u.RoomID(), u.EventData.EventType)
}

type InviteUpdate struct {
	RoomUpdate
	InviteData InviteData
}

func (u *InviteUpdate) Type() string {
	return fmt.Sprintf("InviteUpdate[%s]", u.RoomID())
}

type LeftRoomUpdate struct {
	RoomUpdate
}

func (u *LeftRoomUpdate) Type() string {
	return fmt.Sprintf("LeftRoomUpdate[%s]", u.RoomID())
}

type TypingUpdate struct {
	RoomUpdate
}

func (u *TypingUpdate) Type() string {
	return fmt.Sprintf("TypingUpdate[%s]", u.RoomID())
}

type ReceiptUpdate struct {
	RoomUpdate
	Receipt internal.Receipt
}

func (u *ReceiptUpdate) Type() string {
	return fmt.Sprintf("ReceiptUpdate[%s]", u.RoomID())
}

type UnreadCountUpdate struct {
	RoomUpdate
	HasCountDecreased bool
}

func (u *UnreadCountUpdate) Type() string {
	return fmt.Sprintf("UnreadCountUpdate[%s]", u.RoomID())
}

type AccountDataUpdate struct {
	AccountData []state.AccountData
}

func (u *AccountDataUpdate) Type() string {
	return fmt.Sprintf("AccountDataUpdate len=%v", len(u.AccountData))
}

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
