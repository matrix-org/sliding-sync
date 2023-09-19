package handler

import (
	"context"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3"
)

func Test_connStateLive_shouldIncludeHeroes(t *testing.T) {
	ctx := context.Background()
	list := sync3.NewInternalRequestLists()

	m1 := sync3.RoomConnMetadata{
		RoomMetadata: internal.RoomMetadata{
			RoomID: "!abc",
		},
	}
	m2 := sync3.RoomConnMetadata{
		RoomMetadata: internal.RoomMetadata{
			RoomID: "!def",
		},
	}
	list.SetRoom(m1)
	list.SetRoom(m2)

	list.AssignList(ctx, "all_rooms", &sync3.RequestFilters{}, []string{sync3.SortByName}, false)
	list.AssignList(ctx, "visible_rooms", &sync3.RequestFilters{}, []string{sync3.SortByName}, false)

	boolTrue := true
	tests := []struct {
		name      string
		ConnState *ConnState
		roomID    string
		want      bool
	}{
		{
			name:   "neither in subscription nor in list",
			roomID: "!abc",
			ConnState: &ConnState{
				muxedReq: &sync3.Request{},
			},
		},
		{
			name:   "in room subscription",
			want:   true,
			roomID: "!abc",
			ConnState: &ConnState{
				muxedReq: &sync3.Request{},
				roomSubscriptions: map[string]sync3.RoomSubscription{
					"!abc": {
						Heroes: &boolTrue,
					},
				},
			},
		},
		{
			name:   "in list all_rooms",
			roomID: "!def",
			want:   true,
			ConnState: &ConnState{
				muxedReq: &sync3.Request{
					Lists: map[string]sync3.RequestList{
						"all_rooms": {
							SlowGetAllRooms: &boolTrue,
							RoomSubscription: sync3.RoomSubscription{
								Heroes: &boolTrue,
							},
						},
						"visible_rooms": {
							SlowGetAllRooms: &boolTrue,
						},
					},
				},
				lists: list,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &connStateLive{
				ConnState: tt.ConnState,
			}
			if got := s.shouldIncludeHeroes(tt.roomID); got != tt.want {
				t.Errorf("shouldIncludeHeroes() = %v, want %v", got, tt.want)
			}
		})
	}
}
