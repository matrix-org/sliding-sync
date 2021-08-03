package streams

import (
	"bytes"
	"encoding/json"
)

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
func (r *Request) ApplyDeltas(req2 *Request) (bool, error) {
	// remember the original to tell if there is a diff
	original, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	// marshal the 2nd request to unmarshal it into the original request
	delta, err := json.Marshal(req2)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(delta, r); err != nil {
		return false, err
	}
	// re-marshal r to check if there was a delta
	combined, err := json.Marshal(r)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(original, combined), nil
}
