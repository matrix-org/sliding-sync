package sync3

type List interface {
	IndexOf(roomID string) (int, bool)
	Len() int64
	Sort(sortBy []string) error
	Add(roomID string) bool
	Remove(roomID string) int
	Get(index int) string
}

// CalculateListOps contains the core list moving algorithm. It accepts the client's list of ranges,
// the underlying list on which to perform operations on, and the room which was modified and in what
// way. It returns a list of INSERT/DELETE operations, which may be zero length, as well as which rooms
// are newly added into the window.
//    A,B,C,D,E,F,G,H,I        <-- List
//    `----`    `----`         <-- RequestList.Ranges
//    DEL E | ADD J | CHANGE C <-- ListOp RoomID
// returns:
//    [ {op:DELETE, index:2}, {op:INSERT, index:0, room_id:A} ] <--- ResponseOp
//    [ "A" ] <--- new room subscription, if it wasn't in the window before
//
// This function will modify List to Add/Delete/Sort appropriately.
func CalculateListOps(reqList *RequestList, list List, roomID string, listOp ListOp) (ops []ResponseOp, subs []string) {
	fromIndex, ok := list.IndexOf(roomID)
	if !ok {
		if listOp == ListOpAdd {
			fromIndex = int(list.Len())
		} else {
			return
		}
	}

	newlyAdded := listOp == ListOpAdd
	if listOp == ListOpAdd {
		list.Add(roomID)
	} else if listOp == ListOpDel {
		delIndex := list.Remove(roomID)
		if delIndex >= 0 {
			ops = append(ops, reqList.WriteDeleteOp(delIndex))
			return
		}
	}

	_, wasInsideRange := reqList.Ranges.Inside(int64(fromIndex))
	if newlyAdded {
		wasInsideRange = false // can't be inside the range if this is a new room
	}

	// this should only move exactly 1 room at most as this is called for every single update
	if err := list.Sort(reqList.Sort); err != nil {
		logger.Err(err).Msg("cannot sort list")
	}
	toIndex, _ := list.IndexOf(roomID)

	listFromTos := reqList.CalculateMoveIndexes(fromIndex, toIndex)
	if len(listFromTos) == 0 {
		return
	}

	for _, listFromTo := range listFromTos {
		listFromIndex := listFromTo[0]
		listToIndex := listFromTo[1]
		wasUpdatedRoomInserted := listToIndex == toIndex
		roomID := list.Get(listToIndex)

		swapOp := reqList.WriteSwapOp(roomID, listFromIndex, listToIndex)
		ops = append(ops, swapOp...)

		addedSub := false
		// if a different room is being inserted or the room wasn't previously inside a range, send
		// the entire room data
		if !wasUpdatedRoomInserted || !wasInsideRange {
			subs = append(subs, roomID)
			addedSub = true
		}

		if wasUpdatedRoomInserted && newlyAdded {
			if !addedSub {
				subs = append(subs, roomID)
			}
			// The client needs to know which item in the list to delete to know which direction to
			// shift items (left or right). This means the vast majority of BRAND NEW rooms will result
			// in a swap op even though you could argue that an INSERT[0] or something is sufficient
			// (though bear in mind that we don't always insert to [0]). The one edge case is when
			// there are no items in the list at all. In this case, there is no swap op as there is
			// nothing to swap, but we still need to tell clients about the operation, hence a lone
			// INSERT op.
			// This can be intuitively explained by saying that "if there is a room at this toIndex
			// then we need a DELETE to tell the client whether the room being replaced should shift
			// left or shift right". In absence of a room being there, we just INSERT, which plays
			// nicely with the logic of "if there is nothing at this index position, insert, else expect
			// a delete op to have happened before".
			if swapOp == nil {
				ops = append(ops, reqList.WriteInsertOp(toIndex, roomID))
			}
		}
	}
	return
}
