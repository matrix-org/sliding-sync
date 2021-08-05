package streams

type Response struct {
	Next       string              `json:"next"`
	Typing     *TypingResponse     `json:"typing,omitempty"`
	ToDevice   *ToDeviceResponse   `json:"to_device,omitempty"`
	RoomMember *RoomMemberResponse `json:"room_member,omitempty"`
}
