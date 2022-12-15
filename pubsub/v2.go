package pubsub

import (
	"encoding/json"

	"github.com/matrix-org/sliding-sync/internal"
)

// The channel which has V2* payloads
const ChanV2 = "v2ch"

type V2Listener interface {
	Initialise(p *V2Initialise)
	Accumulate(p *V2Accumulate)
	OnAccountData(p *V2AccountData)
	OnInvite(p *V2InviteRoom)
	OnLeftRoom(p *V2LeaveRoom)
	OnUnreadCounts(p *V2UnreadCounts)
	OnInitialSyncComplete(p *V2InitialSyncComplete)
	OnDeviceData(p *V2DeviceData)
	OnTyping(p *V2Typing)
	OnReceipt(p *V2Receipt)
}

type V2Initialise struct {
	RoomID      string
	SnapshotNID int64
}

func (v V2Initialise) Type() string { return "s" }

type V2Accumulate struct {
	RoomID    string
	PrevBatch string
	EventNIDs []int64
}

func (v V2Accumulate) Type() string { return "a" }

type V2UnreadCounts struct {
	UserID            string
	RoomID            string
	HighlightCount    *int
	NotificationCount *int
}

func (v V2UnreadCounts) Type() string { return "u" }

type V2AccountData struct {
	UserID string
	RoomID string
	Types  []string
}

func (v V2AccountData) Type() string { return "c" }

type V2LeaveRoom struct {
	UserID string
	RoomID string
}

func (v V2LeaveRoom) Type() string { return "l" }

type V2InviteRoom struct {
	UserID string
	RoomID string
}

func (v V2InviteRoom) Type() string { return "i" }

type V2InitialSyncComplete struct {
	UserID   string
	DeviceID string
}

func (v V2InitialSyncComplete) Type() string { return "x" }

type V2DeviceData struct {
	Pos int64
}

func (v V2DeviceData) Type() string { return "d" }

type V2Typing struct {
	RoomID         string
	EphemeralEvent json.RawMessage
}

func (v V2Typing) Type() string { return "t" }

type V2Receipt struct {
	RoomID   string
	Receipts []internal.Receipt
}

func (v V2Receipt) Type() string { return "r" }

type V2Sub struct {
	listener Listener
	receiver V2Listener
}

func NewV2Sub(l Listener, recv V2Listener) *V2Sub {
	return &V2Sub{
		listener: l,
		receiver: recv,
	}
}

func (v *V2Sub) Teardown() {
	v.listener.Close()
}

func (v *V2Sub) onMessage(p Payload) {
	switch p.Type() {
	case V2Receipt{}.Type():
		v.receiver.OnReceipt(p.(*V2Receipt))
	case V2Initialise{}.Type():
		v.receiver.Initialise(p.(*V2Initialise))
	case V2Accumulate{}.Type():
		v.receiver.Accumulate(p.(*V2Accumulate))
	case V2AccountData{}.Type():
		v.receiver.OnAccountData(p.(*V2AccountData))
	case V2InviteRoom{}.Type():
		v.receiver.OnInvite(p.(*V2InviteRoom))
	case V2LeaveRoom{}.Type():
		v.receiver.OnLeftRoom(p.(*V2LeaveRoom))
	case V2UnreadCounts{}.Type():
		v.receiver.OnUnreadCounts(p.(*V2UnreadCounts))
	case V2InitialSyncComplete{}.Type():
		v.receiver.OnInitialSyncComplete(p.(*V2InitialSyncComplete))
	case V2DeviceData{}.Type():
		v.receiver.OnDeviceData(p.(*V2DeviceData))
	case V2Typing{}.Type():
		v.receiver.OnTyping(p.(*V2Typing))
	}
}

func (v *V2Sub) Listen() error {
	return v.listener.Listen(ChanV2, v.onMessage)
}
