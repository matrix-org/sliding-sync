package streams

// Request represents a sync v3 request
//
// A request is made by the combination of the client HTTP request parameters and the stored filters
// on the server.
type Request struct {
	RoomList *FilterRoomList `json:"room_list,omitempty"`
	Typing   *FilterTyping   `json:"typing,omitempty"`
	ToDevice *FilterToDevice `json:"to_device,omitempty"`
}

// ApplyDeltas updates Request with the values in req2. Returns true if there were deltas.
func (r *Request) ApplyDeltas(req2 *Request) bool {
	deltasExist := false
	if req2.RoomList != nil {
		deltasExist = true
		if r.RoomList == nil {
			r.RoomList = req2.RoomList
		} else {
			r.RoomList = r.RoomList.Combine(req2.RoomList)
		}
	}
	if req2.Typing != nil {
		deltasExist = true
		if r.Typing == nil {
			r.Typing = req2.Typing
		} else {
			r.Typing = r.Typing.Combine(req2.Typing)
		}
	}
	if req2.ToDevice != nil {
		deltasExist = true
		if r.ToDevice == nil {
			r.ToDevice = req2.ToDevice
		} else {
			r.ToDevice = r.ToDevice.Combine(req2.ToDevice)
		}
	}
	return deltasExist
}
