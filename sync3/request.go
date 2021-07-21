package sync3

import "github.com/matrix-org/sync-v3/streams"

// Request represents a sync v3 request
//
// A request is made by the combination of the client HTTP request parameters and the stored filters
// on the server.
type Request struct {
	RoomList streams.FilterRoomList `json:"room_list"`
}
