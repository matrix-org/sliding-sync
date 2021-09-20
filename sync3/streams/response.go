package streams

import "encoding/json"

type Response struct {
	Next       string                     `json:"next_batch"`
	Typing     *TypingResponse            `json:"typing,omitempty"`
	ToDevice   *ToDeviceResponse          `json:"to_device,omitempty"`
	RoomMember *RoomMemberResponse        `json:"room_member,omitempty"`
	Events     map[string]json.RawMessage `json:"events,omitempty"`
}
