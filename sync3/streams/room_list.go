package streams

import "github.com/matrix-org/sync-v3/state"

// RoomList represents a stream of room summaries.
// This stream is paginatable.
type RoomList struct {
	storage *state.Storage
}

func NewRoomList(s *state.Storage) *RoomList {
	return &RoomList{s}
}

// FilterRoomList represents a filter on the RoomList stream
type FilterRoomList struct {
	// Which event types should be returned as the latest event in the room.
	// Clients should include only events they know how to render here.
	// Empty set = everything
	SummaryEventTypes []string
	// The number of rooms to return per request.
	// Clients should return at least 1 screen's worth of data (based on viewport size)
	// Server can override this value.
	EntriesPerBatch int
	// The max length of the room name. If the name is higher than this, it will be truncated with
	// unicode ellipsis.
	// Clients should limit the size to how much they can display (e.g 70 chars)
	RoomNameSize int
	// True to include the MXC URI for the room avatar, if it has one.
	IncludeRoomAvatarMXC *bool
}

// Combine two filters together. A new filter is returned.
func (f *FilterRoomList) Combine(new *FilterRoomList) *FilterRoomList {
	combined := &FilterRoomList{}
	// nil slice != 0 length slice
	if new.SummaryEventTypes != nil {
		combined.SummaryEventTypes = new.SummaryEventTypes
	} else {
		combined.SummaryEventTypes = f.SummaryEventTypes
	}
	if new.EntriesPerBatch != 0 {
		combined.EntriesPerBatch = new.EntriesPerBatch
	} else {
		combined.EntriesPerBatch = f.EntriesPerBatch
	}
	if new.RoomNameSize != 0 {
		combined.RoomNameSize = new.RoomNameSize
	} else {
		combined.RoomNameSize = f.RoomNameSize
	}
	// pointer to bool to indicate absence
	if new.IncludeRoomAvatarMXC != nil {
		combined.IncludeRoomAvatarMXC = new.IncludeRoomAvatarMXC
	} else {
		combined.IncludeRoomAvatarMXC = f.IncludeRoomAvatarMXC
	}

	return combined
}

type ControlMessageRoomList struct {
	EntriesPerBatch *int   `json:"entries_per_batch,omitempty"`
	Upcoming        string `json:"upcoming"`
	NextPage        string `json:"next_page"`
}

// Process a room list stream request
func (s *RoomList) Process(userID string, from, to int64, page string, f *FilterRoomList) (ctrl *ControlMessageRoomList, result string, err error) {
	if from == 0 {

	}
	return nil, "", nil
}
