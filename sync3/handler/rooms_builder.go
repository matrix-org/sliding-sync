package handler

import (
	"context"
	"fmt"
	"sort"

	"github.com/matrix-org/sliding-sync/internal"
	"github.com/matrix-org/sliding-sync/sync3"
)

// RoomsBuilder gradually accumulates and mixes data required in order to populate the top-level rooms key
// in the Response. It is not thread-safe and should only be called by the ConnState thread.
//
// The top-level `rooms` key is an amalgamation of:
//   - Room subscriptions
//   - Rooms within all sliding lists.
//
// The purpose of this builder is to remember which rooms we will be returning data for, along with the
// room subscription for that room. This then allows efficient database accesses. For example:
//   - List A will return !a, !b, !c with Room Subscription X
//   - List B will return !b, !c, !d with Room Subscription Y
//   - Room sub for !a with Room Subscription Z
//
// Rather than performing each operation in isolation and query for rooms multiple times (where the
// response data will inevitably be dropped), we can instead amalgamate this into:
//   - Room Subscription X+Z -> !a
//   - Room Subscription X+Y -> !b, !c
//   - Room Subscription Y -> !d
//
// This data will not be wasted when it has been retrieved from the database.
type RoomsBuilder struct {
	subs       []sync3.RoomSubscription
	subToRooms map[int][]string
}

func NewRoomsBuilder() *RoomsBuilder {
	return &RoomsBuilder{
		subToRooms: make(map[int][]string),
	}
}

func (rb *RoomsBuilder) IncludesRoom(roomID string) bool {
	for _, roomIDs := range rb.subToRooms {
		for _, rid := range roomIDs {
			if roomID == rid {
				return true
			}
		}
	}
	return false
}

// Add a room subscription to the builder, e.g from a list or room subscription. This should NOT
// be a combined subscription.
func (rb *RoomsBuilder) AddSubscription(rs sync3.RoomSubscription) (id int) {
	rb.subs = append(rb.subs, rs)
	return len(rb.subs) - 1
}

// Add rooms to the subscription ID previously added. E.g rooms from a list.
func (rb *RoomsBuilder) AddRoomsToSubscription(ctx context.Context, id int, roomIDs []string) {
	internal.AssertWithContext(ctx, "subscription ID is unknown", id < len(rb.subs))
	rb.subToRooms[id] = append(rb.subToRooms[id], roomIDs...)
}

// Work out which subscriptions need to be combined and produce a new set of subscriptions -> room IDs.
// Any given room ID will appear in exactly one BuiltSubscription.
func (rb *RoomsBuilder) BuildSubscriptions() (result []BuiltSubscription) {
	// calculate the inverse (room -> subs)
	roomToSubIDs := make(map[string]map[int]struct{}) // room_id to set of ints
	for subID, roomIDs := range rb.subToRooms {
		for _, roomID := range roomIDs {
			if _, ok := roomToSubIDs[roomID]; !ok {
				roomToSubIDs[roomID] = make(map[int]struct{})
			}
			roomToSubIDs[roomID][subID] = struct{}{}
		}
	}

	// for each room, create a combined subscription (or use an existing subscription), remembering
	// which combined subscriptions we have so we can reuse them
	subToRoomIDs := make(map[*sync3.RoomSubscription][]string) // incl. combined subs
	subIDToSub := make(map[string]*sync3.RoomSubscription)
	for i := range rb.subs {
		subIDToSub[subKey(i)] = &rb.subs[i] // uncombined subscriptions
	}
	for roomID, subIDs := range roomToSubIDs {
		// convert the set into a list
		subIDList := make([]int, 0, len(subIDs))
		for subID := range subIDs {
			subIDList = append(subIDList, subID)
		}
		// calculate the combined sub key and see if it exists already
		sk := subKey(subIDList...)
		combinedSub, ok := subIDToSub[sk]
		if !ok {
			// make a combined room subscription
			var combinedRoomSub *sync3.RoomSubscription
			for _, subID := range subIDList {
				roomSub := rb.subs[subID]
				if combinedRoomSub == nil {
					combinedRoomSub = &roomSub
				} else {
					crs := combinedRoomSub.Combine(roomSub)
					combinedRoomSub = &crs
				}
			}
			// assign it
			subIDToSub[sk] = combinedRoomSub
			combinedSub = combinedRoomSub
		}
		// add this room to this combined sub. Relies on the pointers being the same for combined subs
		subToRoomIDs[combinedSub] = append(subToRoomIDs[combinedSub], roomID)
	}
	// make the subscriptions
	for sub, roomIDs := range subToRoomIDs {
		result = append(result, BuiltSubscription{
			RoomSubscription: *sub,
			RoomIDs:          roomIDs,
		})
	}
	return result
}

func subKey(subIDs ...int) string {
	// we must sort so subscriptions are commutative
	sort.Ints(subIDs)
	return fmt.Sprintf("%v", subIDs)
}

type BuiltSubscription struct {
	RoomSubscription sync3.RoomSubscription
	RoomIDs          []string
}
