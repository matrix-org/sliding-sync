package sync3

import (
	"context"
	"github.com/matrix-org/sliding-sync/internal"
)

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
//
//	A,B,C,D,E,F,G,H,I        <-- List
//	`----`    `----`         <-- RequestList.Ranges
//	DEL E | ADD J | CHANGE C <-- ListOp RoomID
//
// returns:
//
//	[ {op:DELETE, index:2}, {op:INSERT, index:0, room_id:A} ] <--- []ResponseOp
//	[ "A" ] <--- []string, new room subscriptions, if it wasn't in the window before
//
// This function will modify List to Add/Delete/Sort appropriately.
func CalculateListOps(ctx context.Context, reqList *RequestList, list List, roomID string, listOp ListOp) (ops []ResponseOp, subs []string) {
	fromIndex, ok := list.IndexOf(roomID)
	if !ok {
		if listOp == ListOpAdd {
			fromIndex = int(list.Len())
		} else {
			// room doesn't exist and we aren't adding it, nothing to do.
			return
		}
	}
	_, wasInsideRange := reqList.Ranges.Inside(int64(fromIndex))

	var toIndex int
	// modify the list
	switch listOp {
	case ListOpAdd:
		wasInsideRange = false // can't be inside the range if this is a new room
		list.Add(roomID)
		// this should only move exactly 1 room at most as this is called for every single update
		if err := list.Sort(reqList.Sort); err != nil {
			logger.Err(err).Msg("cannot sort list")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		}
		// find the new position of this room
		toIndex, _ = list.IndexOf(roomID)
	case ListOpDel:
		list.Remove(roomID)
		// no need to resort here, everything is in the right order already and just needs a shift
		// there will be no toIndex, set it to the end of the array
		toIndex = int(list.Len()) - 1
		if toIndex == -1 {
			// we are removing the last element of the list
			ops = append(ops, reqList.WriteDeleteOp(0))
			return
		}
	case ListOpChange:
		// this should only move exactly 1 room at most as this is called for every single update
		if err := list.Sort(reqList.Sort); err != nil {
			logger.Err(err).Msg("cannot sort list")
			internal.GetSentryHubFromContextOrDefault(ctx).CaptureException(err)
		}
		// find the new position of this room
		toIndex, _ = list.IndexOf(roomID)
	}

	listFromTos := reqList.CalculateMoveIndexes(fromIndex, toIndex)
	if len(listFromTos) == 0 {
		return
	}

	for _, listFromTo := range listFromTos {
		listFromIndex := listFromTo[0]
		listToIndex := listFromTo[1]
		wasUpdatedRoomInserted := listToIndex == toIndex
		toRoomID := list.Get(listToIndex)
		if toRoomID == roomID && listFromIndex == listToIndex && listOp == ListOpChange && wasInsideRange && len(listFromTos) == 1 {
			// DELETE/INSERT have the same index, we're INSERTing the room that was updated, it was a Change not Add/Delete, it
			// was previously inside the window AND there's just 1 move operation = it's moving to and from the same index so
			// don't send an update.
			continue // no-op move
		}

		if wasUpdatedRoomInserted && listOp == ListOpDel {
			// ignore this insert, as deletions algorithmically just jump to the end of the array.
			// we do this so we can calculate jumps over ranges using the same codepaths as moves.
			isInsertingDeletedRoom := toRoomID == roomID
			// The algorithm needs to know if it should issue an INSERT for the `to` room. The whole
			// "treat deletes as inserts at the end of the array" thing makes it hard to know if it
			// should or should not. This check ensures that we only skip the INSERT if the to-be shifted
			// room was not already sent to the client.
			_, isShiftedRoomAlreadySent := reqList.Ranges.Inside(list.Len())
			if isInsertingDeletedRoom || (listToIndex == int(list.Len()-1) && isShiftedRoomAlreadySent) {
				ops = append(ops, reqList.WriteDeleteOp(listFromIndex))
				continue
			}
		}

		swapOp := reqList.WriteSwapOp(toRoomID, listFromIndex, listToIndex)
		ops = append(ops, swapOp...)

		addedSub := false
		// if a different room is being inserted or the room wasn't previously inside a range, send
		// the entire room data
		if !wasUpdatedRoomInserted || !wasInsideRange {
			subs = append(subs, toRoomID)
			addedSub = true
		}

		if wasUpdatedRoomInserted && listOp == ListOpAdd {
			if !addedSub {
				subs = append(subs, toRoomID)
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
				ops = append(ops, reqList.WriteInsertOp(toIndex, toRoomID))
			}
		}
	}
	return
}
