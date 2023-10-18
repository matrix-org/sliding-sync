package m

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/tidwall/gjson"
)

type RespMatcher func(res *sync3.Response) error
type ListMatcher func(list sync3.ResponseList) error
type OpMatcher func(op sync3.ResponseOp) error
type RoomMatcher func(r sync3.Room) error

// LogRoom builds a matcher that always succeeds. As a side-effect, it pretty-prints
// the given room to the test log. This is useful when debugging a test.
func LogRoom(t *testing.T) RoomMatcher {
	return func(room sync3.Room) error {
		dump, _ := json.MarshalIndent(room, "", "    ")
		t.Logf("Response was: %s", dump)
		return nil
	}
}

func MatchRoomName(name string) RoomMatcher {
	return func(r sync3.Room) error {
		if name == "" {
			return nil
		}
		if r.Name != name {
			return fmt.Errorf("name mismatch, got %s want %s", r.Name, name)
		}
		return nil
	}
}

// MatchRoomAvatar builds a RoomMatcher which checks that the given room response has
// set the room's avatar to the given value.
func MatchRoomAvatar(wantAvatar string) RoomMatcher {
	return func(r sync3.Room) error {
		if string(r.AvatarChange) != wantAvatar {
			return fmt.Errorf("MatchRoomAvatar: got \"%s\" want \"%s\"", r.AvatarChange, wantAvatar)
		}
		return nil
	}
}

// MatchRoomUnsetAvatar builds a RoomMatcher which checks that the given room has no
// avatar, or has had its avatar deleted.
func MatchRoomUnsetAvatar() RoomMatcher {
	return func(r sync3.Room) error {
		if r.AvatarChange != sync3.DeletedAvatar {
			return fmt.Errorf("MatchRoomAvatar: got \"%s\" want \"%s\"", r.AvatarChange, sync3.DeletedAvatar)
		}
		return nil
	}
}

// MatchRoomUnchangedAvatar builds a RoomMatcher which checks that the given room has no
// change to its avatar, or has had its avatar deleted.
func MatchRoomUnchangedAvatar() RoomMatcher {
	return func(r sync3.Room) error {
		if r.AvatarChange != sync3.UnchangedAvatar {
			return fmt.Errorf("MatchRoomAvatar: got \"%s\" want \"%s\"", r.AvatarChange, sync3.UnchangedAvatar)
		}
		return nil
	}
}

func MatchJoinCount(count int) RoomMatcher {
	return func(r sync3.Room) error {
		if r.JoinedCount != count {
			return fmt.Errorf("MatchJoinCount: got %v want %v", r.JoinedCount, count)
		}
		return nil
	}
}

func MatchNoInviteCount() RoomMatcher {
	return func(r sync3.Room) error {
		if r.InvitedCount != nil {
			return fmt.Errorf("MatchInviteCount: invited_count is present when it should be missing: val=%v", *r.InvitedCount)
		}
		return nil
	}
}

func MatchInviteCount(count int) RoomMatcher {
	return func(r sync3.Room) error {
		if r.InvitedCount == nil {
			return fmt.Errorf("MatchInviteCount: invited_count is missing")
		}
		if *r.InvitedCount != count {
			return fmt.Errorf("MatchInviteCount: got %v want %v", *r.InvitedCount, count)
		}
		return nil
	}
}

func MatchNumLive(numLive int) RoomMatcher {
	return func(r sync3.Room) error {
		if r.NumLive != numLive {
			return fmt.Errorf("MatchNumLive: got %v want %v", r.NumLive, numLive)
		}
		return nil
	}
}

func MatchRoomRequiredState(events []json.RawMessage) RoomMatcher {
	return func(r sync3.Room) error {
		if len(r.RequiredState) != len(events) {
			return fmt.Errorf("required state length mismatch, got %d want %d", len(r.RequiredState), len(events))
		}
		// allow any ordering for required state
		for _, want := range events {
			found := false
			for _, got := range r.RequiredState {
				if bytes.Equal(got, want) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("required state want event %v but it does not exist", string(want))
			}
		}
		return nil
	}
}
func MatchRoomInviteState(events []json.RawMessage) RoomMatcher {
	return func(r sync3.Room) error {
		if len(r.InviteState) != len(events) {
			return fmt.Errorf("invite state length mismatch, got %d want %d", len(r.InviteState), len(events))
		}
		// allow any ordering for required state
		for _, want := range events {
			found := false
			for _, got := range r.InviteState {
				if bytes.Equal(got, want) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("required state want event %v but it does not exist", string(want))
			}
		}
		return nil
	}
}

func MatchRoomHasInviteState() RoomMatcher {
	return func(r sync3.Room) error {
		if len(r.InviteState) == 0 {
			return fmt.Errorf("missing or empty invite state, expected at least one piece of invite state")
		}
		return nil
	}
}

func MatchRoomLacksInviteState() RoomMatcher {
	return func(r sync3.Room) error {
		if len(r.InviteState) > 0 {
			return fmt.Errorf("invite state present, but expected no invite state")
		}
		return nil
	}
}

// Similar to MatchRoomTimeline but takes the last n events of `events` and only checks with the last
// n events of the timeline.
func MatchRoomTimelineMostRecent(n int, events []json.RawMessage) RoomMatcher {
	subset := events[len(events)-n:]
	return func(r sync3.Room) error {
		if len(r.Timeline) < len(subset) {
			return fmt.Errorf("MatchRoomTimelineMostRecent: timeline length mismatch: got %d want at least %d", len(r.Timeline), len(subset))
		}
		gotSubset := r.Timeline[len(r.Timeline)-n:]
		for i := range gotSubset {
			if !bytes.Equal(gotSubset[i], subset[i]) {
				return fmt.Errorf("timeline[%d]\ngot  %v \nwant %v", i, string(gotSubset[i]), string(subset[i]))
			}
		}
		return nil
	}
}

func MatchRoomPrevBatch(prevBatch string) RoomMatcher {
	return func(r sync3.Room) error {
		if prevBatch != r.PrevBatch {
			return fmt.Errorf("MatchRoomPrevBatch: got %v want %v", r.PrevBatch, prevBatch)
		}
		return nil
	}
}

// Match the timeline with exactly these events in exactly this order
func MatchRoomTimeline(events []json.RawMessage) RoomMatcher {
	return func(r sync3.Room) error {
		if len(r.Timeline) != len(events) {
			return fmt.Errorf("timeline length mismatch: got %d want %d", len(r.Timeline), len(events))
		}
		for i := range r.Timeline {
			if !bytes.Equal(r.Timeline[i], events[i]) {
				return fmt.Errorf("timeline[%d]\ngot  %v \nwant %v", i, string(r.Timeline[i]), string(events[i]))
			}
		}
		return nil
	}
}

func MatchRoomHighlightCount(count int64) RoomMatcher {
	return func(r sync3.Room) error {
		if r.HighlightCount != count {
			return fmt.Errorf("highlight count mismatch, got %d want %d", r.HighlightCount, count)
		}
		return nil
	}
}
func MatchRoomNotificationCount(count int64) RoomMatcher {
	return func(r sync3.Room) error {
		if r.NotificationCount != count {
			return fmt.Errorf("notification count mismatch, got %d want %d", r.NotificationCount, count)
		}
		return nil
	}
}

func MatchRoomInitial(initial bool) RoomMatcher {
	return func(r sync3.Room) error {
		if r.Initial != initial {
			return fmt.Errorf("MatchRoomInitial: got %v want %v", r.Initial, initial)
		}
		return nil
	}
}

func MatchV3Count(wantCount int) ListMatcher {
	return func(res sync3.ResponseList) error {
		if res.Count != wantCount {
			return fmt.Errorf("list got count %d want %d", res.Count, wantCount)
		}
		return nil
	}
}

func MatchRoomSubscriptionsStrict(wantSubs map[string][]RoomMatcher) RespMatcher {
	return func(res *sync3.Response) error {
		if len(res.Rooms) != len(wantSubs) {
			return fmt.Errorf("MatchRoomSubscriptionsStrict: strict length on: got %v subs want %v", len(res.Rooms), len(wantSubs))
		}
		for roomID, matchers := range wantSubs {
			room, ok := res.Rooms[roomID]
			if !ok {
				return fmt.Errorf("MatchRoomSubscriptionsStrict: want sub for %s but it was missing", roomID)
			}
			for _, m := range matchers {
				if err := m(room); err != nil {
					return fmt.Errorf("MatchRoomSubscriptionsStrict[%s]: %s", roomID, err)
				}
			}
		}
		return nil
	}
}

func MatchRoomSubscription(roomID string, matchers ...RoomMatcher) RespMatcher {
	return func(res *sync3.Response) error {
		room, ok := res.Rooms[roomID]
		if !ok {
			return fmt.Errorf("MatchRoomSubscription[%s]: want sub but it was missing", roomID)
		}
		errs := make([]error, 0, len(matchers))
		for _, m := range matchers {
			if err := m(room); err != nil {
				errs = append(errs, fmt.Errorf("%s: %s", roomID, err))
			}
		}

		if len(errs) > 0 {
			return fmt.Errorf("MatchRoomSubscription: %d errors:\n%w", len(errs), errors.Join(errs...))
		}
		return nil
	}
}

func MatchRoomSubscriptions(wantSubs map[string][]RoomMatcher) RespMatcher {
	return func(res *sync3.Response) error {
		for roomID, matchers := range wantSubs {
			room, ok := res.Rooms[roomID]
			if !ok {
				return fmt.Errorf("MatchRoomSubscriptions: want sub for %s but it was missing", roomID)
			}
			for _, m := range matchers {
				if err := m(room); err != nil {
					return fmt.Errorf("MatchRoomSubscriptions[%s]: %s", roomID, err)
				}
			}
		}
		return nil
	}
}

func MatchNoE2EEExtension() RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.E2EE != nil {
			return fmt.Errorf("MatchNoE2EEExtension: got E2EE extension: %+v", res.Extensions.E2EE)
		}
		return nil
	}
}

func MatchNoReceiptsExtension() RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.Receipts != nil {
			return fmt.Errorf("MatchNoReceiptsExtension: got Receipts extension: %+v", res.Extensions.Receipts)
		}
		return nil
	}
}

func MatchOTKCounts(otkCounts map[string]int) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.E2EE == nil {
			return fmt.Errorf("MatchOTKCounts: no E2EE extension present")
		}
		if !reflect.DeepEqual(res.Extensions.E2EE.OTKCounts, otkCounts) {
			return fmt.Errorf("MatchOTKCounts: got %v want %v", res.Extensions.E2EE.OTKCounts, otkCounts)
		}
		return nil
	}
}

func MatchFallbackKeyTypes(fallbackKeyTypes []string) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.E2EE == nil {
			return fmt.Errorf("MatchFallbackKeyTypes: no E2EE extension present")
		}
		if !reflect.DeepEqual(res.Extensions.E2EE.FallbackKeyTypes, fallbackKeyTypes) {
			return fmt.Errorf("MatchFallbackKeyTypes: got %v want %v", res.Extensions.E2EE.FallbackKeyTypes, fallbackKeyTypes)
		}
		return nil
	}
}

func MatchDeviceLists(changed, left []string) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.E2EE == nil {
			return fmt.Errorf("MatchDeviceLists: no E2EE extension present")
		}
		if res.Extensions.E2EE.DeviceLists == nil {
			return fmt.Errorf("MatchDeviceLists: no device lists present")
		}
		if !reflect.DeepEqual(res.Extensions.E2EE.DeviceLists.Changed, changed) {
			return fmt.Errorf("MatchDeviceLists: got changed: %v want %v", res.Extensions.E2EE.DeviceLists.Changed, changed)
		}
		if !reflect.DeepEqual(res.Extensions.E2EE.DeviceLists.Left, left) {
			return fmt.Errorf("MatchDeviceLists: got left: %v want %v", res.Extensions.E2EE.DeviceLists.Left, left)
		}
		return nil
	}
}

func MatchToDeviceMessages(wantMsgs []json.RawMessage) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.ToDevice == nil {
			return fmt.Errorf("MatchToDeviceMessages: missing to_device extension")
		}
		if len(res.Extensions.ToDevice.Events) != len(wantMsgs) {
			return fmt.Errorf("MatchToDeviceMessages: got %d events, want %d", len(res.Extensions.ToDevice.Events), len(wantMsgs))
		}
		for i := 0; i < len(wantMsgs); i++ {
			if !reflect.DeepEqual(res.Extensions.ToDevice.Events[i], wantMsgs[i]) {
				return fmt.Errorf("MatchToDeviceMessages[%d]: got %v want %v", i, string(res.Extensions.ToDevice.Events[i]), string(wantMsgs[i]))
			}
		}
		return nil
	}
}

func MatchV3SyncOp(start, end int64, roomIDs []string, anyOrder ...bool) OpMatcher {
	allowAnyOrder := len(anyOrder) > 0 && anyOrder[0]
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpSync {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpSync)
		}
		oper := op.(*sync3.ResponseOpRange)
		if oper.Range[0] != start {
			return fmt.Errorf("%s: got start %d want %d", sync3.OpSync, oper.Range[0], start)
		}
		if oper.Range[1] != end {
			return fmt.Errorf("%s: got end %d want %d", sync3.OpSync, oper.Range[1], end)
		}
		if allowAnyOrder {
			sort.Strings(oper.RoomIDs)
			sort.Strings(roomIDs)
		}
		if !reflect.DeepEqual(roomIDs, oper.RoomIDs) {
			return fmt.Errorf("%s: got rooms %v want %v", sync3.OpSync, oper.RoomIDs, roomIDs)
		}
		return nil
	}
}

func MatchV3SyncOpFn(fn func(op *sync3.ResponseOpRange) error) OpMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpSync {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpSync)
		}
		oper := op.(*sync3.ResponseOpRange)
		return fn(oper)
	}
}

func MatchV3InsertOp(roomIndex int, roomID string) OpMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpInsert {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpInsert)
		}
		oper := op.(*sync3.ResponseOpSingle)
		if *oper.Index != roomIndex {
			return fmt.Errorf("%s: got index %d want %d", sync3.OpInsert, *oper.Index, roomIndex)
		}
		if oper.RoomID != roomID {
			return fmt.Errorf("%s: got %s want %s", sync3.OpInsert, oper.RoomID, roomID)
		}
		return nil
	}
}

func MatchV3DeleteOp(roomIndex int) OpMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpDelete {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpDelete)
		}
		oper := op.(*sync3.ResponseOpSingle)
		if *oper.Index != roomIndex {
			return fmt.Errorf("%s: got room index %d want %d", sync3.OpDelete, *oper.Index, roomIndex)
		}
		return nil
	}
}

func MatchV3InvalidateOp(start, end int64) OpMatcher {
	return func(op sync3.ResponseOp) error {
		if op.Op() != sync3.OpInvalidate {
			return fmt.Errorf("op: %s != %s", op.Op(), sync3.OpInvalidate)
		}
		oper := op.(*sync3.ResponseOpRange)
		if oper.Range[0] != start {
			return fmt.Errorf("%s: got start %d want %d", sync3.OpInvalidate, oper.Range[0], start)
		}
		if oper.Range[1] != end {
			return fmt.Errorf("%s: got end %d want %d", sync3.OpInvalidate, oper.Range[1], end)
		}
		return nil
	}
}

func MatchNoV3Ops() RespMatcher {
	return func(res *sync3.Response) error {
		for key, l := range res.Lists {
			if len(l.Ops) > 0 {
				return fmt.Errorf("MatchNoV3Ops: list %v got %d ops", key, len(l.Ops))
			}
		}
		return nil
	}
}

func MatchV3Ops(matchOps ...OpMatcher) ListMatcher {
	return func(res sync3.ResponseList) error {
		if len(matchOps) != len(res.Ops) {
			return fmt.Errorf("MatchV3Ops: got %d ops want %d", len(res.Ops), len(matchOps))
		}
		for i := range res.Ops {
			op := res.Ops[i]
			if err := matchOps[i](op); err != nil {
				return fmt.Errorf("MatchV3Ops: op[%d](%s) - %s", i, op.Op(), err)
			}
		}
		return nil
	}
}

func MatchTyping(roomID string, wantUserIDs []string) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.Typing == nil {
			return fmt.Errorf("MatchTyping: no typing extension")
		}
		if len(res.Extensions.Typing.Rooms) == 0 || res.Extensions.Typing.Rooms[roomID] == nil {
			serialised, _ := json.Marshal(res.Extensions.Typing)
			return fmt.Errorf("MatchTyping: missing room %s: got %s", roomID, serialised)
		}
		sort.Strings(wantUserIDs)
		ev := res.Extensions.Typing.Rooms[roomID]
		userIDs := gjson.ParseBytes(ev).Get("content.user_ids").Array()
		gotUserIDs := make([]string, len(userIDs))
		for i := range userIDs {
			gotUserIDs[i] = userIDs[i].Str
		}
		sort.Strings(gotUserIDs)
		if !reflect.DeepEqual(gotUserIDs, wantUserIDs) {
			return fmt.Errorf("MatchTyping: mismatched typing users, got %v want %v", gotUserIDs, wantUserIDs)
		}
		return nil
	}
}

func MatchNotTyping(roomID string, dontWantUserIDs []string) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.Typing == nil {
			return nil
		}
		if len(res.Extensions.Typing.Rooms) == 0 || res.Extensions.Typing.Rooms[roomID] == nil {
			return nil
		}
		ev := res.Extensions.Typing.Rooms[roomID]
		typingIDs := gjson.ParseBytes(ev).Get("content.user_ids").Array()

		for _, dontWantID := range dontWantUserIDs {
			for _, typingID := range typingIDs {
				// Quick and dirty: report the first mismatch we see.
				if dontWantID == typingID.Str {
					return fmt.Errorf("MatchTyping: user %s should not be typing in %s, but is", dontWantID, roomID)
				}
			}
		}
		return nil
	}
}

type Receipt struct {
	EventID  string
	UserID   string
	Type     string
	ThreadID string
}

func sortReceipts(receipts []Receipt) {
	sort.Slice(receipts, func(i, j int) bool {
		keyi := receipts[i].EventID + receipts[i].UserID + receipts[i].Type + receipts[i].ThreadID
		keyj := receipts[j].EventID + receipts[j].UserID + receipts[j].Type + receipts[j].ThreadID
		return keyi < keyj
	})
}

// MatchReceipts builds a matcher which asserts that a sync response has the expected
// set of read receipts in a given room is the expected set of `wantReceipts`.
//
// The match fails if:
//   - there is no receipts extension in the sync response,
//   - the room is missing from the sync response and `wantReceipts` is nonempty,
//   - the room is present in the sync response but has a different set of receipts
//     to `wantReceipts`.
func MatchReceipts(roomID string, wantReceipts []Receipt) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.Receipts == nil {
			return fmt.Errorf("MatchReceipts: no receipts extension")
		}
		if len(res.Extensions.Receipts.Rooms) == 0 || res.Extensions.Receipts.Rooms[roomID] == nil {
			if len(wantReceipts) == 0 {
				return nil // want nothing
			}
			return fmt.Errorf("MatchReceipts: missing room %s: got %+v", roomID, res.Extensions.Receipts)
		}
		var gotReceipts []Receipt
		ev := res.Extensions.Receipts.Rooms[roomID]
		gjson.ParseBytes(ev).Get("content").ForEach(func(key, value gjson.Result) bool {
			eventID := key.Str
			value.ForEach(func(key, value gjson.Result) bool {
				receiptType := key.Str
				value.ForEach(func(key, value gjson.Result) bool {
					userID := key.Str
					threadID := value.Get("thread_id").Str
					gotReceipts = append(gotReceipts, Receipt{
						EventID:  eventID,
						UserID:   userID,
						Type:     receiptType,
						ThreadID: threadID,
					})
					return true
				})
				return true
			})
			return true
		})
		sortReceipts(gotReceipts)
		sortReceipts(wantReceipts)
		if !reflect.DeepEqual(gotReceipts, wantReceipts) {
			return fmt.Errorf("MatchReceipts: wrong receipts, got %v want %v", gotReceipts, wantReceipts)
		}
		return nil
	}
}

// MatchAccountData builds a matcher which asserts that the account data in a sync
// response /exactly/ matches the given `globals` and `rooms`, up to ordering.
//
// - If there is no account data extension in the response, the matche fails.
// - If globals is non-nil:
//   - if globals is not equal to the global account data, the match fails.
//     Equality is determined using EqualAnyOrder.
//
// - If rooms is non-nil:
//   - If the set of given rooms differs from the set of rooms present in the account
//     data response, the match fails.
//   - If a given room's account data events are not equal to its account data events
//     in the sync response, the match fails. Again, equality is determined using
//     EqualAnyOrder.
func MatchAccountData(globals []json.RawMessage, rooms map[string][]json.RawMessage) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.AccountData == nil {
			return fmt.Errorf("MatchAccountData: no account_data extension")
		}
		if len(globals) > 0 {
			if err := EqualAnyOrder(res.Extensions.AccountData.Global, globals); err != nil {
				return fmt.Errorf("MatchAccountData[global]: %s", err)
			}
		}
		if len(rooms) > 0 {
			if len(rooms) != len(res.Extensions.AccountData.Rooms) {
				return fmt.Errorf("MatchAccountData: got %d rooms with account data, want %d", len(res.Extensions.AccountData.Rooms), len(rooms))
			}
			for roomID := range rooms {
				gots := res.Extensions.AccountData.Rooms[roomID]
				if gots == nil {
					return fmt.Errorf("MatchAccountData: want room account data for %s but it was missing", roomID)
				}
				if err := EqualAnyOrder(gots, rooms[roomID]); err != nil {
					return fmt.Errorf("MatchAccountData[room]: %s", err)
				}
			}
		}
		return nil
	}
}

// MatchHasGlobalAccountData builds a matcher which asserts that the given event is
// present in a global account data response.
func MatchHasGlobalAccountData(want json.RawMessage) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.AccountData == nil {
			return fmt.Errorf("No account data section in sync response")
		}
		for _, msg := range res.Extensions.AccountData.Global {
			if bytes.Equal(msg, want) {
				return nil
			}
		}

		return fmt.Errorf("could not find %s in global account data", want)
	}
}

// MatchNoGlobalAccountData builds a matcher which asserts that no global account data
// is present in a sync response.
func MatchNoGlobalAccountData() RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.AccountData == nil {
			return nil
		}
		accountDataEvents := res.Extensions.AccountData.Global
		if len(accountDataEvents) > 0 {
			return fmt.Errorf("MatchNoGlobalAccountData: got %d account data events, but expected none", len(accountDataEvents))
		}
		return nil
	}
}

// MatchNoRoomAccountData builds a matcher which asserts that none of the given roomIDs
// have room account data in a sync response.
func MatchNoRoomAccountData(roomIDs []string) RespMatcher {
	return func(res *sync3.Response) error {
		if res.Extensions.AccountData == nil {
			return nil
		}
		for _, roomID := range roomIDs {
			// quick and dirty: complain the first time we see something we shouldn't
			roomData := res.Extensions.AccountData.Rooms[roomID]
			if roomData != nil {
				return fmt.Errorf("MatchNoRoomAccountData: got account data for %s, but expected it to be missing", roomID)
			}
		}
		return nil
	}
}

// LogResponse builds a matcher that always succeeds. As a side-effect, it pretty-prints
// the given sync response to the test log. This is useful when debugging a test.
func LogResponse(t *testing.T) RespMatcher {
	return func(res *sync3.Response) error {
		dump, _ := json.MarshalIndent(res, "", "    ")
		t.Logf("Response was: %s", dump)
		return nil
	}
}

// LogRooms is like LogResponse, but only logs the rooms section of the response.
func LogRooms(t *testing.T) RespMatcher {
	return func(res *sync3.Response) error {
		dump, _ := json.MarshalIndent(res.Rooms, "", "    ")
		t.Logf("Response rooms were: %s", dump)
		return nil
	}
}

func CheckList(listKey string, res sync3.ResponseList, matchers ...ListMatcher) error {
	for _, m := range matchers {
		if err := m(res); err != nil {
			return fmt.Errorf("MatchList[%v]: %v", listKey, err)
		}
	}
	return nil
}

func MatchTxnID(txnID string) RespMatcher {
	return func(res *sync3.Response) error {
		if txnID != res.TxnID {
			return fmt.Errorf("MatchTxnID: got %v want %v", res.TxnID, txnID)
		}
		return nil
	}
}

func MatchList(listKey string, matchers ...ListMatcher) RespMatcher {
	return func(res *sync3.Response) error {
		if _, exists := res.Lists[listKey]; !exists {
			return fmt.Errorf("MatchSingleList: key %v does not exist, got %d lists", listKey, len(res.Lists))
		}
		list := res.Lists[listKey]
		return CheckList(listKey, list, matchers...)
	}
}

func MatchLists(matchers map[string][]ListMatcher) RespMatcher {
	return func(res *sync3.Response) error {
		if len(matchers) != len(res.Lists) {
			return fmt.Errorf("MatchLists: got %d matchers for %d lists", len(matchers), len(res.Lists))
		}
		for listKey, matchersForList := range matchers {
			if err := CheckList(listKey, res.Lists[listKey], matchersForList...); err != nil {
				return fmt.Errorf("MatchLists[%v]: %v", listKey, err)
			}
		}
		return nil
	}
}

const AnsiRedForeground = "\x1b[31m"
const AnsiResetForeground = "\x1b[39m"

func MatchResponse(t *testing.T, res *sync3.Response, matchers ...RespMatcher) {
	t.Helper()
	for _, m := range matchers {
		err := m(res)
		if err != nil {
			b, _ := json.MarshalIndent(res, "", "    ")
			t.Errorf("%vMatchResponse: %s\n%s%v", AnsiRedForeground, err, string(b), AnsiResetForeground)
		}
	}
}

func CheckRoom(r sync3.Room, matchers ...RoomMatcher) error {
	for _, m := range matchers {
		if err := m(r); err != nil {
			return fmt.Errorf("MatchRoom : %s", err)
		}
	}
	return nil
}

func EqualAnyOrder(got, want []json.RawMessage) error {
	if len(got) != len(want) {
		return fmt.Errorf("EqualAnyOrder: got %d, want %d", len(got), len(want))
	}
	sort.Slice(got, func(i, j int) bool {
		return string(got[i]) < string(got[j])
	})
	sort.Slice(want, func(i, j int) bool {
		return string(want[i]) < string(want[j])
	})
	for i := range got {
		if !reflect.DeepEqual(got[i], want[i]) {
			return fmt.Errorf("EqualAnyOrder: [%d] got %v want %v", i, string(got[i]), string(want[i]))
		}
	}
	return nil
}
