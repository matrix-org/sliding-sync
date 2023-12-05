package syncv3_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/matrix-org/complement/b"
	"github.com/matrix-org/sliding-sync/sync3/extensions"
	"github.com/tidwall/gjson"

	"github.com/matrix-org/sliding-sync/sync3"
	"github.com/matrix-org/sliding-sync/testutils/m"
)

// Test that multiple lists can be independently scrolled through
func TestMultipleLists(t *testing.T) {
	alice := registerNewUser(t)

	// make 10 encrypted rooms and make 10 unencrypted rooms. [0] is most recent
	var encryptedRoomIDs []string
	var unencryptedRoomIDs []string
	for i := 0; i < 10; i++ {
		unencryptedRoomID := alice.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
		})
		unencryptedRoomIDs = append([]string{unencryptedRoomID}, unencryptedRoomIDs...) // push in array
		encryptedRoomID := alice.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
			"initial_state": []b.Event{
				NewEncryptionEvent(),
			},
		})
		encryptedRoomIDs = append([]string{encryptedRoomID}, encryptedRoomIDs...) // push in array
		time.Sleep(time.Millisecond)                                              // ensure timestamp changes
	}

	// request 2 lists, one set encrypted, one set unencrypted
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"enc": {
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
			"unenc": {
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolFalse,
				},
			},
		},
	})

	m.MatchResponse(t, res,
		m.MatchLists(map[string][]m.ListMatcher{
			"enc": {
				m.MatchV3Count(len(encryptedRoomIDs)),
				m.MatchV3Ops(m.MatchV3SyncOp(0, 2, encryptedRoomIDs[:3])),
			},
			"unenc": {
				m.MatchV3Count(len(unencryptedRoomIDs)),
				m.MatchV3Ops(m.MatchV3SyncOp(0, 2, unencryptedRoomIDs[:3])),
			},
		}),
		m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
			encryptedRoomIDs[0]:   {},
			encryptedRoomIDs[1]:   {},
			encryptedRoomIDs[2]:   {},
			unencryptedRoomIDs[0]: {},
			unencryptedRoomIDs[1]: {},
			unencryptedRoomIDs[2]: {},
		}),
	)

	// now scroll one of the lists
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"enc": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
			"unenc": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
					[2]int64{3, 5}, // next 3 rooms
				},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"enc": {
			m.MatchV3Count(len(encryptedRoomIDs)),
		},
		"unenc": {
			m.MatchV3Count(len(unencryptedRoomIDs)),
			m.MatchV3Ops(
				m.MatchV3SyncOp(3, 5, unencryptedRoomIDs[3:6]),
			),
		},
	}), m.MatchRoomSubscriptionsStrict(map[string][]m.RoomMatcher{
		unencryptedRoomIDs[3]: {},
		unencryptedRoomIDs[4]: {},
		unencryptedRoomIDs[5]: {},
	}))

	// now shift the last/oldest unencrypted room to an encrypted room and make sure both lists update
	alice.SendEventSynced(t, unencryptedRoomIDs[len(unencryptedRoomIDs)-1], NewEncryptionEvent())
	// update our source of truth: the last unencrypted room is now the first encrypted room
	encryptedRoomIDs = append([]string{unencryptedRoomIDs[len(unencryptedRoomIDs)-1]}, encryptedRoomIDs...)
	unencryptedRoomIDs = unencryptedRoomIDs[:len(unencryptedRoomIDs)-1]

	// We are tracking the first few encrypted rooms so we expect list 0 to update
	// However we do not track old unencrypted rooms so we expect no change in list 1
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"enc": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
			"unenc": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
					[2]int64{3, 5}, // next 3 rooms
				},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"enc": {
			m.MatchV3Count(len(encryptedRoomIDs)),
			m.MatchV3Ops(
				m.MatchV3DeleteOp(2),
				m.MatchV3InsertOp(0, encryptedRoomIDs[0]),
			),
		},
		"unenc": {
			m.MatchV3Count(len(unencryptedRoomIDs)),
		},
	}))
}

// Test that bumps only update a single list and not both. Regression test for when
// DM rooms get bumped they appeared in the is_dm:false list.
func TestMultipleListsDMUpdate(t *testing.T) {
	alice := registerNewUser(t)
	var dmRoomIDs []string
	var groupRoomIDs []string
	dmContent := map[string]interface{}{} // user_id -> [room_id]
	// make 5 group rooms and make 5 DMs rooms. Room 0 is most recent to ease checks
	for i := 0; i < 5; i++ {
		dmUserID := fmt.Sprintf("@dm_%d:synapse", i) // TODO: domain brittle
		groupRoomID := alice.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
		})
		groupRoomIDs = append([]string{groupRoomID}, groupRoomIDs...) // push in array
		dmRoomID := alice.MustCreateRoom(t, map[string]interface{}{
			"preset":    "trusted_private_chat",
			"is_direct": true,
			"invite":    []string{dmUserID},
		})
		dmRoomIDs = append([]string{dmRoomID}, dmRoomIDs...) // push in array
		dmContent[dmUserID] = []string{dmRoomID}
		time.Sleep(time.Millisecond) // ensure timestamp changes
	}
	// set the account data
	alice.MustSetGlobalAccountData(t, "m.direct", dmContent)

	// request 2 lists, one set DM, one set no DM
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"dm": {
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsDM: &boolTrue,
				},
			},
			"nodm": {
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
				Filters: &sync3.RequestFilters{
					IsDM: &boolFalse,
				},
			},
		},
	})

	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"dm": {
			m.MatchV3Count(len(dmRoomIDs)),
			m.MatchV3Ops(m.MatchV3SyncOp(0, 2, dmRoomIDs[:3])),
		},
		"nodm": {
			m.MatchV3Count(len(groupRoomIDs)),
			m.MatchV3Ops(m.MatchV3SyncOp(0, 2, groupRoomIDs[:3])),
		},
	}))

	// now bring the last DM room to the top with a notif
	pingEventID := alice.SendEventSynced(t, dmRoomIDs[len(dmRoomIDs)-1], b.Event{
		Type:    "m.room.message",
		Content: map[string]interface{}{"body": "ping", "msgtype": "m.text"},
	})
	// update our source of truth: swap the last and first elements
	dmRoomIDs[0], dmRoomIDs[len(dmRoomIDs)-1] = dmRoomIDs[len(dmRoomIDs)-1], dmRoomIDs[0]

	// now get the delta: only the DM room should change
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"dm": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
			"nodm": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms still
				},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		"dm": {
			m.MatchV3Count(len(dmRoomIDs)),
			m.MatchV3Ops(
				m.MatchV3DeleteOp(2),
				m.MatchV3InsertOp(0, dmRoomIDs[0]),
			),
		},
		"nodm": {
			m.MatchV3Count(len(groupRoomIDs)),
		},
	}), m.MatchRoomSubscription(dmRoomIDs[0], MatchRoomTimelineMostRecent(1, []Event{
		{
			Type: "m.room.message",
			ID:   pingEventID,
		},
	})))
}

// Test that a new list can be added mid-connection
func TestNewListMidConnection(t *testing.T) {
	alice := registerNewUser(t)
	var roomIDs []string
	// make rooms
	for i := 0; i < 4; i++ {
		roomID := alice.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
		})
		roomIDs = append([]string{roomID}, roomIDs...) // push in array
		time.Sleep(time.Millisecond)                   // ensure timestamp changes
	}

	// first request no list
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{},
	})
	m.MatchResponse(t, res, m.MatchLists(nil))

	// now add a list
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1,
				},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(len(roomIDs)), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 2, roomIDs[:3]),
	)))
}

// Tests that if a room appears in >1 list that we union room subscriptions correctly.
func TestMultipleOverlappingLists(t *testing.T) {
	alice := registerNewUser(t)

	var allRoomIDs []string
	var encryptedRoomIDs []string
	var dmRoomIDs []string
	dmContent := map[string]interface{}{} // user_id -> [room_id]
	dmUserID := "@bob:synapse"
	// make 3 encrypted rooms, 3 encrypted/dm rooms, 3 dm rooms.
	// [0] is the newest room.
	for i := 9; i >= 0; i-- {
		isEncrypted := i < 6
		isDM := i >= 3

		createContent := map[string]interface{}{
			"preset": "private_chat",
		}
		if isEncrypted {
			createContent["initial_state"] = []b.Event{
				NewEncryptionEvent(),
			}
		}
		if isDM {
			createContent["is_direct"] = true
			createContent["invite"] = []string{dmUserID}
		}
		roomID := alice.MustCreateRoom(t, createContent)
		time.Sleep(time.Millisecond)
		if isDM {
			var roomIDs []string
			roomIDsInt, ok := dmContent[dmUserID]
			if ok {
				roomIDs = roomIDsInt.([]string)
			}
			dmContent[dmUserID] = append(roomIDs, roomID)
			dmRoomIDs = append([]string{roomID}, dmRoomIDs...)
		}
		if isEncrypted {
			encryptedRoomIDs = append([]string{roomID}, encryptedRoomIDs...)
		}
		allRoomIDs = append([]string{roomID}, allRoomIDs...) // push entries so [0] is newest
	}
	// set the account data
	t.Logf("DM rooms: %v", dmRoomIDs)
	t.Logf("Encrypted rooms: %v", encryptedRoomIDs)
	alice.MustSetGlobalAccountData(t, "m.direct", dmContent)

	// seed the proxy: so we can get timeline correctly as it uses limit:1 initially.
	alice.SlidingSync(t, sync3.Request{})

	// send messages to track timeline. The most recent messages are:
	// - ENCRYPTION EVENT (if set)
	// - DM INVITE EVENT (if set)
	// - This ping message (always)
	roomToEventID := make(map[string]string, len(allRoomIDs))
	for i := len(allRoomIDs) - 1; i >= 0; i-- {
		roomToEventID[allRoomIDs[i]] = alice.SendEventSynced(t, allRoomIDs[i], b.Event{
			Type:    "m.room.message",
			Content: map[string]interface{}{"body": "ping", "msgtype": "m.text"},
		})
	}
	lastEventID := roomToEventID[allRoomIDs[0]]
	alice.SlidingSyncUntilEventID(t, "", allRoomIDs[0], lastEventID)

	// request 2 lists: one DMs, one encrypted. The room subscriptions are different so they should be UNION'd correctly.
	// We request 5 rooms to ensure there is some overlap but not total overlap:
	//   newest   top 5 DM
	//   v     .-----------.
	//   E E E ED* ED* D D D D
	//   `-----------`
	//      top 5 Encrypted
	//
	// Rooms with * are union'd
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"enc": {
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 4}, // first 5 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 2, // pull in the ping msg + some state event depending on the room type
					RequiredState: [][2]string{
						{"m.room.join_rules", ""},
					},
				},
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
			"dm": {
				Sort: []string{sync3.SortByRecency},
				Ranges: sync3.SliceRanges{
					[2]int64{0, 4}, // first 5 rooms
				},
				RoomSubscription: sync3.RoomSubscription{
					TimelineLimit: 1, // pull in ping message only
					RequiredState: [][2]string{
						{"m.room.power_levels", ""},
					},
				},
				Filters: &sync3.RequestFilters{
					IsDM: &boolTrue,
				},
			},
		},
	})

	m.MatchResponse(t, res,
		m.MatchList("enc", m.MatchV3Ops(m.MatchV3SyncOp(0, 4, encryptedRoomIDs[:5]))),
		m.MatchList("dm", m.MatchV3Ops(m.MatchV3SyncOp(0, 4, dmRoomIDs[:5]))),
		m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
			// encrypted rooms just come from the encrypted only list
			encryptedRoomIDs[0]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.join_rules",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimeline([]Event{
					{Type: "m.room.encryption", StateKey: ptr("")},
					{ID: roomToEventID[encryptedRoomIDs[0]]},
				}),
			},
			encryptedRoomIDs[1]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.join_rules",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimeline([]Event{
					{Type: "m.room.encryption", StateKey: ptr("")},
					{ID: roomToEventID[encryptedRoomIDs[1]]},
				}),
			},
			encryptedRoomIDs[2]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.join_rules",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimeline([]Event{
					{Type: "m.room.encryption", StateKey: ptr("")},
					{ID: roomToEventID[encryptedRoomIDs[2]]},
				}),
			},
			// overlapping with DM rooms
			encryptedRoomIDs[3]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.join_rules",
						StateKey: ptr(""),
					},
					{
						Type:     "m.room.power_levels",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimeline([]Event{
					{Type: "m.room.member", StateKey: ptr(dmUserID)},
					{ID: roomToEventID[encryptedRoomIDs[3]]},
				}),
			},
			encryptedRoomIDs[4]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.join_rules",
						StateKey: ptr(""),
					},
					{
						Type:     "m.room.power_levels",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimeline([]Event{
					{Type: "m.room.member", StateKey: ptr(dmUserID)},
					{ID: roomToEventID[encryptedRoomIDs[4]]},
				}),
			},
			// DM only rooms
			dmRoomIDs[2]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.power_levels",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimelineMostRecent(1, []Event{{ID: roomToEventID[dmRoomIDs[2]]}}),
			},
			dmRoomIDs[3]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.power_levels",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimelineMostRecent(1, []Event{{ID: roomToEventID[dmRoomIDs[3]]}}),
			},
			dmRoomIDs[4]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredStateStrict([]Event{
					{
						Type:     "m.room.power_levels",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimelineMostRecent(1, []Event{{ID: roomToEventID[dmRoomIDs[4]]}}),
			},
		}),
	)
}

// Regression test for a panic when new rooms were live-streamed to the client in Element-Web
func TestNot500OnNewRooms(t *testing.T) {
	boolTrue := true
	boolFalse := false
	mSpace := "m.space"
	alice := registerNewUser(t)
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				SlowGetAllRooms: &boolTrue,
				Filters: &sync3.RequestFilters{
					RoomTypes: []*string{&mSpace},
				},
			},
		},
	})
	alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				SlowGetAllRooms: &boolTrue,
				Filters: &sync3.RequestFilters{
					RoomTypes: []*string{&mSpace},
				},
			},
			"b": {
				Filters: &sync3.RequestFilters{
					IsDM: &boolFalse,
				},
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	// should not 500
	alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				SlowGetAllRooms: &boolTrue,
			},
			"b": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
}

// Regression test for room name calculations, which could be incorrect for new rooms due to caches
// not being populated yet e.g "and 1 others" or "Empty room" when a room name has been set.
func TestNewRoomNameCalculations(t *testing.T) {
	alice := registerNewUser(t)
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				SlowGetAllRooms: &boolTrue,
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(0)))

	// create 10 room in parallel and at the same time spam sliding sync to ensure we get bits of
	// rooms before they are fully loaded.
	numRooms := 10
	ch := make(chan int, numRooms)
	var roomIDToName sync.Map

	// start the goroutines
	for i := 0; i < numRooms; i++ {
		go func() {
			for i := range ch {
				roomName := fmt.Sprintf("room %d", i)
				roomID := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": roomName})
				roomIDToName.Store(roomID, roomName)
			}
		}()
	}
	// inject the work
	for i := 0; i < numRooms; i++ {
		ch <- i
	}
	close(ch)
	seenRoomNames := make(map[string]string)
	start := time.Now()
	var err error
	for {
		res = alice.SlidingSync(t, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					SlowGetAllRooms: &boolTrue,
				},
			},
		}, WithPos(res.Pos))
		for roomID, sub := range res.Rooms {
			if sub.Name != "" {
				seenRoomNames[roomID] = sub.Name
			}
		}
		if time.Since(start) > 15*time.Second {
			t.Errorf("timed out, did not see all rooms, seen %d/%d", len(seenRoomNames), numRooms)
			break
		}
		// do assertions and bail if they all pass
		err = nil
		seenRooms := 0
		roomIDToName.Range(func(key, value interface{}) bool {
			seenRooms++
			createRoomID := key.(string)
			name := value.(string)
			gotName := seenRoomNames[createRoomID]
			if name != gotName {
				err = fmt.Errorf("[%s: got %s want %s] %w", createRoomID, gotName, name, err)
			}
			return true
		})
		if seenRooms != numRooms {
			continue // wait for all /createRoom calls to return
		}
		if err == nil {
			t.Logf("%+v\n", seenRoomNames)
			break // we saw all the rooms with the right names
		}
	}
	if err != nil {
		// we didn't see all the right room names after timeout secs
		t.Errorf(err.Error())
	}
}

// Regression test for when you swap from sorting by recency to sorting by name and it didn't take effect.
func TestChangeSortOrder(t *testing.T) {
	alice := registerNewUser(t)

	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Sort:   []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{
					RequiredState: [][2]string{{"m.room.name", ""}},
				},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchRoomSubscriptionsStrict(nil), m.MatchNoV3Ops())
	roomNames := []string{
		"Kiwi", "Lemon", "Apple", "Orange",
	}
	roomIDs := make([]string, len(roomNames))
	gotNameToIDs := make(map[string]string)

	waitFor := func(roomID, roomName string) {
		start := time.Now()
		for {
			if time.Since(start) > time.Second {
				t.Fatalf("didn't see room name '%s' for room %s", roomName, roomID)
				break
			}
			res = alice.SlidingSync(t, sync3.Request{
				Lists: map[string]sync3.RequestList{
					"a": {
						Ranges: sync3.SliceRanges{{0, 20}},
					},
				},
			}, WithPos(res.Pos))
			for roomID, sub := range res.Rooms {
				gotNameToIDs[sub.Name] = roomID
			}
			if gotNameToIDs[roomName] == roomID {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	for i, name := range roomNames {
		roomIDs[i] = alice.MustCreateRoom(t, map[string]interface{}{
			"name": name,
		})
		// we cannot guarantee we will see the right state yet, so just keep track of the room names
		waitFor(roomIDs[i], name)
	}

	// now change the sort order: first send the request up then keep hitting sliding sync until
	// we see the txn ID confirming it has been applied
	txnID := "a"
	res = alice.SlidingSync(t, sync3.Request{
		TxnID: "a",
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
				Sort:   []string{sync3.SortByName},
			},
		},
	}, WithPos(res.Pos))
	for res.TxnID != txnID {
		res = alice.SlidingSync(t, sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Ranges: sync3.SliceRanges{{0, 20}},
				},
			},
		}, WithPos(res.Pos))
	}

	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(4), m.MatchV3Ops(
		m.MatchV3InvalidateOp(0, 3),
		m.MatchV3SyncOp(0, 3, []string{gotNameToIDs["Apple"], gotNameToIDs["Kiwi"], gotNameToIDs["Lemon"], gotNameToIDs["Orange"]}),
	)))
}

// Regression test that a window can be shrunk and the INVALIDATE appears correctly for the low/high ends of the range
func TestShrinkRange(t *testing.T) {
	alice := registerNewUser(t)
	var roomIDs []string // most recent first
	for i := 0; i < 10; i++ {
		time.Sleep(time.Millisecond) // ensure creation timestamp changes
		roomIDs = append([]string{alice.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
			"name":   fmt.Sprintf("Room %d", i),
		})}, roomIDs...)
	}
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(10), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 9, roomIDs),
	)))
	// now shrink the window on both ends
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{2, 6}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(10), m.MatchV3Ops(
		m.MatchV3InvalidateOp(0, 1),
		m.MatchV3InvalidateOp(7, 9),
	)))
}

// Regression test that you can expand a window without it causing problems. Previously, it would cause
// a spurious {"op":"SYNC","range":[11,20],"room_ids":["!jHVHDxEWqTIcbDYoah:synapse"]} op for a bad index
func TestExpandRange(t *testing.T) {
	alice := registerNewUser(t)
	var roomIDs []string // most recent first
	for i := 0; i < 10; i++ {
		time.Sleep(time.Millisecond) // ensure creation timestamp changes
		roomIDs = append([]string{alice.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
			"name":   fmt.Sprintf("Room %d", i),
		})}, roomIDs...)
	}
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 10}},
			},
		},
	})
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(10), m.MatchV3Ops(
		m.MatchV3SyncOp(0, 9, roomIDs),
	)))
	// now expand the window
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Ranges: sync3.SliceRanges{{0, 20}},
			},
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res, m.MatchList("a", m.MatchV3Count(10), m.MatchV3Ops()))
}

// Regression test for Element X which has 2 identical lists and then changes the ranges in weird ways,
// causing the proxy to send back bad data. The request data here matches what EX sent.
func TestMultipleSameList(t *testing.T) {
	alice := registerNewUser(t)
	// the account had 16 rooms, so do this now
	var roomIDs []string // most recent first
	for i := 0; i < 16; i++ {
		time.Sleep(time.Millisecond) // ensure creation timestamp changes
		roomIDs = append([]string{alice.MustCreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
			"name":   fmt.Sprintf("Room %d", i),
		})}, roomIDs...)
	}
	firstList := sync3.RequestList{
		Sort:   []string{sync3.SortByRecency, sync3.SortByName},
		Ranges: sync3.SliceRanges{{0, 20}},
		RoomSubscription: sync3.RoomSubscription{
			RequiredState: [][2]string{{"m.room.avatar", ""}, {"m.room.encryption", ""}},
			TimelineLimit: 10,
		},
	}
	secondList := sync3.RequestList{
		Sort:   []string{sync3.SortByRecency, sync3.SortByName},
		Ranges: sync3.SliceRanges{{0, 16}},
		RoomSubscription: sync3.RoomSubscription{
			RequiredState: [][2]string{{"m.room.avatar", ""}, {"m.room.encryption", ""}},
		},
	}
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"1": firstList, "2": secondList,
		},
	})
	m.MatchResponse(t, res,
		m.MatchList("1", m.MatchV3Count(16), m.MatchV3Ops(
			m.MatchV3SyncOp(0, 15, roomIDs, false),
		)),
		m.MatchList("2", m.MatchV3Count(16), m.MatchV3Ops(
			m.MatchV3SyncOp(0, 15, roomIDs, false),
		)),
	)
	// now change both list ranges in a valid but strange way, and get back bad responses
	firstList.Ranges = sync3.SliceRanges{{2, 15}}  // from [0,20]
	secondList.Ranges = sync3.SliceRanges{{0, 20}} // from [0,16]
	res = alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"1": firstList, "2": secondList,
		},
	}, WithPos(res.Pos))
	m.MatchResponse(t, res,
		m.MatchList("1", m.MatchV3Count(16), m.MatchV3Ops(
			m.MatchV3InvalidateOp(0, 1),
		)),
		m.MatchList("2", m.MatchV3Count(16), m.MatchV3Ops()),
	)
}

// NB: assumes bump_event_types is sticky
func TestBumpEventTypesHandling(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")
	charlie := registerNamedUser(t, "charlie")

	t.Log("Alice creates two rooms")
	room1 := alice.MustCreateRoom(
		t,
		map[string]interface{}{
			"preset": "public_chat",
			"name":   "room1",
		},
	)
	room2 := alice.MustCreateRoom(
		t,
		map[string]interface{}{
			"preset": "public_chat",
			"name":   "room2",
		},
	)
	t.Logf("room1=%s room2=%s", room1, room2)
	t.Log("Bob joins both rooms.")
	bob.JoinRoom(t, room1, nil)
	bob.JoinRoom(t, room2, nil)

	t.Log("Bob sends a message in room 2 then room 1.")
	bob.SendEventSynced(t, room2, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "Hi room 2",
			"msgtype": "m.text",
		},
	})
	bob.SendEventSynced(t, room1, b.Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "Hello world",
			"msgtype": "m.text",
		},
	})

	t.Log("Alice requests a sliding sync that bumps rooms on messages only.")
	aliceReqList := sync3.RequestList{
		Sort:   []string{sync3.SortByRecency, sync3.SortByName},
		Ranges: sync3.SliceRanges{{0, 20}},
		RoomSubscription: sync3.RoomSubscription{
			RequiredState: [][2]string{{"m.room.avatar", ""}, {"m.room.encryption", ""}},
			TimelineLimit: 10,
		},
		BumpEventTypes: []string{"m.room.message", "m.room.encrypted"},
	}
	aliceSyncRequest := sync3.Request{
		Lists: map[string]sync3.RequestList{
			"alice_list": aliceReqList,
		},
	}
	// This sync should include both of Bob's messages. The proxy will make an initial
	// V2 sync to the HS, which should include the latest event in both rooms.
	// TODO: we could capture the event IDs above and assert this explicitly.
	aliceRes := alice.SlidingSync(t, aliceSyncRequest)

	t.Log("Alice's sync response should include room1 ahead of room 2.")
	matchRoom1ThenRoom2 := []m.ListMatcher{
		m.MatchV3Count(2),
		m.MatchV3Ops(
			m.MatchV3SyncOp(0, 1, []string{room1, room2}, false),
		),
	}
	m.MatchResponse(t, aliceRes, m.MatchList("alice_list", matchRoom1ThenRoom2...))

	t.Log("Bob requests a sliding sync that bumps rooms on messages and memberships.")
	bobReqList := sync3.RequestList{
		Sort:   []string{sync3.SortByRecency, sync3.SortByName},
		Ranges: sync3.SliceRanges{{0, 20}},
		RoomSubscription: sync3.RoomSubscription{
			RequiredState: [][2]string{{"m.room.avatar", ""}, {"m.room.encryption", ""}},
			TimelineLimit: 10,
		},
		BumpEventTypes: []string{"m.room.message", "m.room.encrypted", "m.room.member"},
	}
	bobSyncRequest := sync3.Request{
		Lists: map[string]sync3.RequestList{
			"bob_list": bobReqList,
		},
	}
	bobRes := bob.SlidingSync(t, bobSyncRequest)

	t.Log("Bob should also see room 1 ahead of room 2 in his sliding sync response.")
	m.MatchResponse(t, bobRes, m.MatchList("bob_list", matchRoom1ThenRoom2...))

	t.Log("Charlie joins room 2.")
	charlie.JoinRoom(t, room2, nil)

	t.Log("Alice syncs until she sees Charlie's membership.")
	aliceRes = alice.SlidingSyncUntilMembership(t, aliceRes.Pos, room2, charlie, "join")

	t.Log("Alice shouldn't see any rooms' positions change.")
	m.MatchResponse(
		t,
		aliceRes,
		m.MatchList("alice_list", m.MatchV3Count(2)),
		m.MatchNoV3Ops(),
	)

	t.Log("Bob syncs until he sees Charlie's membership.")
	bobRes = bob.SlidingSyncUntilMembership(t, bobRes.Pos, room2, charlie, "join")

	t.Log("Bob should see room 2 at the top of his list.")
	matchBobSeesRoom2Bumped := m.MatchList("bob_list",
		m.MatchV3Count(2),
		m.MatchV3Ops(
			m.MatchV3DeleteOp(1),
			m.MatchV3InsertOp(0, room2),
		),
	)

	m.MatchResponse(t, bobRes, matchBobSeesRoom2Bumped)

	// The read receipt stuff here specifically checks for the bug in
	// https://github.com/matrix-org/sliding-sync/issues/83
	aliceRoom2Timeline := aliceRes.Rooms[room2].Timeline
	aliceLastSeenEvent := aliceRoom2Timeline[len(aliceRoom2Timeline)-1]
	aliceLastSeenEventID := gjson.ParseBytes(aliceLastSeenEvent).Get("event_id").Str
	if aliceLastSeenEventID == "" {
		t.Error("Could not find event ID for the last event in Alice's timeline.")
	}
	t.Log("Alice marks herself as having seen Charlie's join.")
	alice.SendReceipt(t, room2, aliceLastSeenEventID, "m.read")

	t.Log("Alice syncs until she sees her receipt. At no point should see see any room list operations.")
	alice.SlidingSyncUntil(
		t,
		aliceRes.Pos,
		sync3.Request{Extensions: extensions.Request{
			Receipts: &extensions.ReceiptsRequest{
				Core: extensions.Core{Enabled: &boolTrue},
			},
		}},
		func(response *sync3.Response) error {
			if err := m.MatchNoV3Ops()(response); err != nil {
				t.Fatalf("expected no ops while waiting for receipt: %s", err)
			}
			matchReceipt := m.MatchReceipts(room2, []m.Receipt{{
				EventID: aliceLastSeenEventID,
				UserID:  alice.UserID,
				Type:    "m.read",
			}})
			return matchReceipt(response)
		},
	)

}

func TestBumpEventTypesInOverlappingLists(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")

	t.Log("Alice creates four rooms")
	room1 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room1"})
	room2 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room2"})
	room3 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room3"})
	room4 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "room4"})

	t.Log("Alice writes a message in all four rooms.")
	// Note: all lists bump on messages, so this will ensure the recency order is sensible.
	helloWorld := map[string]interface{}{"body": "Hello world", "msgtype": "m.text"}
	alice.Unsafe_SendEventUnsynced(t, room1, b.Event{Type: "m.room.message", Content: helloWorld})
	alice.Unsafe_SendEventUnsynced(t, room2, b.Event{Type: "m.room.message", Content: helloWorld})
	alice.Unsafe_SendEventUnsynced(t, room3, b.Event{Type: "m.room.message", Content: helloWorld})
	alice.SendEventSynced(t, room4, b.Event{Type: "m.room.message", Content: helloWorld})

	t.Log("Alice requests a sync with three lists: one bumping on messages, a second bumping on messages and memberships, and a third bumping on all events.")
	const listMsg = "message"
	const listMsgMember = "message_membership"
	const listAll = "all"
	req := sync3.Request{
		Lists: map[string]sync3.RequestList{
			listMsg: {
				Sort:             []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{TimelineLimit: 10},
				Ranges:           sync3.SliceRanges{{0, 10}},
				BumpEventTypes:   []string{"m.room.message"},
			},
			listMsgMember: {
				Sort:             []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{TimelineLimit: 10},
				Ranges:           sync3.SliceRanges{{0, 10}},
				BumpEventTypes:   []string{"m.room.message", "m.room.member"},
			},
			listAll: {
				Sort:             []string{sync3.SortByRecency},
				RoomSubscription: sync3.RoomSubscription{TimelineLimit: 10},
				Ranges:           sync3.SliceRanges{{0, 10}},
				BumpEventTypes:   nil,
			},
		},
	}
	res := alice.SlidingSync(t, req)

	t.Log("Alice sees the rooms in order 4, 3, 2, 1.")
	// Note: first sync, so first poll, so should see all four message events.
	see4321 := []m.ListMatcher{
		m.MatchV3Count(4),
		m.MatchV3Ops(m.MatchV3SyncOp(0, 3, []string{room4, room3, room2, room1}, false)),
	}
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		listMsg:       see4321,
		listMsgMember: see4321,
		listAll:       see4321,
	}))

	t.Log("Bob joins room 1. Alice syncs until she sees Bob's join.")
	bob.JoinRoom(t, room1, nil)
	res = alice.SlidingSyncUntilMembership(t, res.Pos, room1, bob, "join")

	t.Logf("Alice should see room1 bumped in the lists %s and %s, but not %s", listMsgMember, listAll, listMsg)
	noMovement := []m.ListMatcher{
		m.MatchV3Count(4),
		m.MatchV3Ops(),
	}
	bumpToTop := func(roomID string, fromIdx int) []m.ListMatcher {
		return []m.ListMatcher{
			m.MatchV3Count(4),
			m.MatchV3Ops(m.MatchV3DeleteOp(fromIdx), m.MatchV3InsertOp(0, roomID)),
		}
	}

	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		listMsg:       noMovement,          // 4321
		listMsgMember: bumpToTop(room1, 3), // 4321 -> 1432
		listAll:       bumpToTop(room1, 3), // 4321 -> 1432
	}))

	t.Log("Alice sets a room topic in room 3, and syncs until she sees the topic.")
	topicEventID := alice.Unsafe_SendEventUnsynced(t, room3, b.Event{
		Type:     "m.room.topic",
		StateKey: ptr(""),
		Content:  map[string]interface{}{"topic": "spicy meatballs"},
	})
	res = alice.SlidingSyncUntilEventID(t, res.Pos, room3, topicEventID)

	t.Logf("Alice sees room3 bump in the %s list only", listAll)
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		listMsg:       noMovement,          // 4321
		listMsgMember: noMovement,          // 1432
		listAll:       bumpToTop(room3, 2), // 1432 -> 3142
	}))

	t.Logf("Alice sends a message in room 2, and syncs until she sees it.")
	msgEventID := alice.Unsafe_SendEventUnsynced(t, room2, b.Event{Type: "m.room.message", Content: helloWorld})
	res = alice.SlidingSyncUntilEventID(t, res.Pos, room2, msgEventID)

	t.Logf("Alice sees room2 bump in all lists")
	m.MatchResponse(t, res, m.MatchLists(map[string][]m.ListMatcher{
		listMsg:       bumpToTop(room2, 2), // 4321 -> 2431
		listMsgMember: bumpToTop(room2, 3), // 1432 -> 2143
		listAll:       bumpToTop(room2, 3), // 3142 -> 2314
	}))

}

func TestBumpEventTypesDoesntLeakOnNewConnAfterJoin(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")

	t.Log("Alice creates a room and sends a secret state event.")
	room1 := alice.MustCreateRoom(
		t,
		map[string]interface{}{
			"preset": "public_chat",
			"name":   "room1",
		},
	)
	alice.Unsafe_SendEventUnsynced(t, room1, b.Event{
		Type:     "secret",
		StateKey: ptr(""),
		Content:  map[string]interface{}{},
	})

	t.Log("Bob creates a room and sends a secret state event.")
	time.Sleep(1 * time.Millisecond)
	room2 := bob.MustCreateRoom(
		t,
		map[string]interface{}{
			"preset": "public_chat",
			"name":   "room1",
		},
	)
	bob.Unsafe_SendEventUnsynced(t, room2, b.Event{
		Type:     "secret",
		StateKey: ptr(""),
		Content:  map[string]interface{}{},
	})

	t.Log("Alice invites Bob, who accepts.")
	alice.InviteRoom(t, room1, bob.UserID)
	bob.JoinRoom(t, room1, nil)

	t.Log("Bob sliding syncs, requesting that rooms are bumped on the secret event type.")
	res := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{},
				Ranges:           sync3.SliceRanges{{0, 1}},
				Sort:             []string{sync3.SortByRecency},
				BumpEventTypes:   []string{"secret"},
			},
		},
	})

	t.Log("Bob should see room1 ahead of room2.")
	// The order in which things happen:
	// 1. alice: room1 secret event
	// 2. bob:   room2 secret event
	// 3. alice: invite bob to room 1
	// 4. bob:   join room 1
	// Bob can only see (2), (3) and (4), which means that room 1 has had most recent activity.
	// (If we use the secret events' timestamps alone, without considering what Bob has
	// permission to see, we will only consider (1) and (2), which would mean room 2
	// has had the most recent activity.)
	m.MatchResponse(
		t,
		res,
		m.MatchList(
			"a",
			m.MatchV3Count(2),
			m.MatchV3Ops(m.MatchV3SyncOp(0, 1, []string{room1, room2})),
		),
	)
}

// Like TestBumpEventTypesDoesntLeakOnNewConnAfterJoin, but Bob never accepts the invite.
func TestBumpEventTypesDoesntLeakOnNewConnAfterInvite(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")

	t.Log("Alice creates a room and sends a secret state event.")
	room1 := alice.MustCreateRoom(
		t,
		map[string]interface{}{
			"preset": "public_chat",
			"name":   "room1",
		},
	)
	alice.Unsafe_SendEventUnsynced(t, room1, b.Event{
		Type:     "secret",
		StateKey: ptr(""),
		Content:  map[string]interface{}{},
	})

	t.Log("Bob creates a room and sends a secret state event.")
	time.Sleep(1 * time.Millisecond)
	room2 := bob.MustCreateRoom(
		t,
		map[string]interface{}{
			"preset": "public_chat",
			"name":   "room1",
		},
	)
	bob.Unsafe_SendEventUnsynced(t, room2, b.Event{
		Type:     "secret",
		StateKey: ptr(""),
		Content:  map[string]interface{}{},
	})

	t.Log("Alice invites Bob, who does not respond.")
	alice.InviteRoom(t, room1, bob.UserID)

	t.Log("Bob sliding syncs, requesting that rooms are bumped on the secret event type.")
	res := bob.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				RoomSubscription: sync3.RoomSubscription{},
				Ranges:           sync3.SliceRanges{{0, 1}},
				Sort:             []string{sync3.SortByRecency},
				BumpEventTypes:   []string{"secret"},
			},
		},
	})

	t.Log("Bob should see room1 ahead of room2.")
	// The order in which things happen:
	// 1. alice: room1 secret event
	// 2. bob:   room2 secret event
	// 3. alice: invite bob to room 1
	// Bob can only see (2) and (3), which means that room 1 has had most recent activity.
	// (If we use the secret events' timestamps alone, without considering what Bob has
	// permission to see, we will only consider (1) and (2), which would mean room 2
	// has had the most recent activity.)
	m.MatchResponse(
		t,
		res,
		m.MatchList(
			"a",
			m.MatchV3Count(2),
			m.MatchV3Ops(m.MatchV3SyncOp(0, 1, []string{room1, room2})),
		),
	)
}

// Tests the scenario described at
// https://github.com/matrix-org/sliding-sync/pull/58#discussion_r1159850458
func TestRangeOutsideTotalRooms(t *testing.T) {
	alice := registerNewUser(t)

	t.Log("Alice makes three public rooms.")
	room0 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "A"})
	room1 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "B"})
	room2 := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": "C"})

	t.Log("Alice initial syncs, requesting room ranges [0, 1] and [8, 9]")
	syncRes := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"a": {
				Sort:   []string{sync3.SortByName},
				Ranges: sync3.SliceRanges{{0, 1}, {8, 9}},
			},
		},
	})

	t.Log("Alice should only see rooms 0–1 in the sync response.")
	m.MatchResponse(
		t,
		syncRes,
		m.MatchList(
			"a",
			m.MatchV3Count(3),
			m.MatchV3Ops(
				m.MatchV3SyncOp(0, 1, []string{room0, room1}),
			),
		),
	)

	t.Log("Alice changes the sort order")
	syncRes = alice.SlidingSync(
		t,
		sync3.Request{
			Lists: map[string]sync3.RequestList{
				"a": {
					Sort: []string{sync3.SortByRecency},
				},
			},
		},
		WithPos(syncRes.Pos),
	)
	m.MatchResponse(
		t,
		syncRes,
		m.MatchList(
			"a",
			m.MatchV3Count(3),
			m.MatchV3Ops(
				m.MatchV3InvalidateOp(0, 1),
				m.MatchV3SyncOp(0, 1, []string{room2, room1}),
			),
		),
	)
}

// Nicked from Synapse's tests, see
// https://github.com/matrix-org/synapse/blob/2cacd0849a02d43f88b6c15ee862398159ab827c/tests/test_utils/__init__.py#L154-L161
// Resolution: 1×1, MIME type: image/png, Extension: png, Size: 67 B
var smallPNG = []byte(
	"\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82",
)

func TestAvatarFieldInRoomResponse(t *testing.T) {
	alice := registerNamedUser(t, "alice")
	bob := registerNamedUser(t, "bob")
	chris := registerNamedUser(t, "chris")

	avatarURLs := map[string]struct{}{}
	uploadAvatar := func(client *CSAPI, filename string) string {
		avatar := alice.UploadContent(t, smallPNG, filename, "image/png")
		if _, exists := avatarURLs[avatar]; exists {
			t.Fatalf("New avatar %s has already been uploaded", avatar)
		}
		t.Logf("%s is uploaded as %s", filename, avatar)
		avatarURLs[avatar] = struct{}{}
		return avatar
	}

	t.Log("Alice, Bob and Chris upload and set an avatar.")
	aliceAvatar := uploadAvatar(alice, "alice.png")
	bobAvatar := uploadAvatar(bob, "bob.png")
	chrisAvatar := uploadAvatar(chris, "chris.png")

	alice.SetAvatar(t, aliceAvatar)
	bob.SetAvatar(t, bobAvatar)
	chris.SetAvatar(t, chrisAvatar)

	t.Log("Alice makes a public room, a DM with herself, a DM with Bob, a DM with Chris, and a group-DM with Bob and Chris.")
	public := alice.MustCreateRoom(t, map[string]interface{}{"preset": "public_chat"})
	// TODO: you can create a DM with yourself e.g. as below. It probably ought to have
	//       your own face as an avatar.
	// dmAlice := alice.MustCreateRoom(t, map[string]interface{}{
	//  "preset":    "trusted_private_chat",
	// 	"is_direct": true,
	//  })
	dmBob := alice.MustCreateRoom(t, map[string]interface{}{
		"preset":    "trusted_private_chat",
		"is_direct": true,
		"invite":    []string{bob.UserID},
	})
	dmChris := alice.MustCreateRoom(t, map[string]interface{}{
		"preset":    "trusted_private_chat",
		"is_direct": true,
		"invite":    []string{chris.UserID},
	})
	dmBobChris := alice.MustCreateRoom(t, map[string]interface{}{
		"preset":    "trusted_private_chat",
		"is_direct": true,
		"invite":    []string{bob.UserID, chris.UserID},
	})

	alice.MustSetGlobalAccountData(t, "m.direct", map[string]any{
		bob.UserID:   []string{dmBob, dmBobChris},
		chris.UserID: []string{dmChris, dmBobChris},
	})

	t.Logf("Rooms:\npublic=%s\ndmBob=%s\ndmChris=%s\ndmBobChris=%s", public, dmBob, dmChris, dmBobChris)
	t.Log("Bob accepts his invites. Chris accepts none.")
	bob.JoinRoom(t, dmBob, nil)
	bob.JoinRoom(t, dmBobChris, nil)

	t.Log("Alice makes an initial sliding sync.")
	res := alice.SlidingSync(t, sync3.Request{
		Lists: map[string]sync3.RequestList{
			"rooms": {
				Ranges: sync3.SliceRanges{{0, 4}},
			},
		},
	})

	t.Log("Alice should see each room in the sync response with an appropriate avatar and DM flag")
	m.MatchResponse(
		t,
		res,
		m.MatchRoomSubscription(public, m.MatchRoomUnsetAvatar(), m.MatchRoomIsDM(false)),
		m.MatchRoomSubscription(dmBob, m.MatchRoomAvatar(bob.AvatarURL), m.MatchRoomIsDM(true)),
		m.MatchRoomSubscription(dmChris, m.MatchRoomAvatar(chris.AvatarURL), m.MatchRoomIsDM(true)),
		m.MatchRoomSubscription(dmBobChris, m.MatchRoomUnsetAvatar(), m.MatchRoomIsDM(true)),
	)

	t.Run("Avatar not resent on message", func(t *testing.T) {
		t.Log("Bob sends a sentinel message.")
		sentinel := bob.SendEventSynced(t, dmBob, b.Event{
			Type: "m.room.message",
			Content: map[string]interface{}{
				"body":    "Hello world",
				"msgtype": "m.text",
			},
		})

		t.Log("Alice syncs until she sees the sentinel. She should not see the DM avatar change.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, func(response *sync3.Response) error {
			matchNoAvatarChange := m.MatchRoomSubscription(dmBob, m.MatchRoomUnchangedAvatar())
			if err := matchNoAvatarChange(response); err != nil {
				t.Fatalf("Saw DM avatar change: %s", err)
			}
			matchSentinel := m.MatchRoomSubscription(dmBob, MatchRoomTimelineMostRecent(1, []Event{{ID: sentinel}}))
			return matchSentinel(response)
		})
	})

	t.Run("DM declined", func(t *testing.T) {
		t.Log("Chris leaves his DM with Alice.")
		chris.LeaveRoom(t, dmChris)

		t.Log("Alice syncs until she sees Chris's leave.")
		res = alice.SlidingSyncUntilMembership(t, res.Pos, dmChris, chris, "leave")

		t.Log("Alice sees Chris's avatar vanish.")
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmChris, m.MatchRoomUnsetAvatar()))
	})

	t.Run("Group DM declined", func(t *testing.T) {
		t.Log("Chris leaves his group DM with Alice and Bob.")
		chris.LeaveRoom(t, dmBobChris)

		t.Log("Alice syncs until she sees Chris's leave.")
		res = alice.SlidingSyncUntilMembership(t, res.Pos, dmBobChris, chris, "leave")

		t.Log("Alice sees the room's avatar change to Bob's avatar.")
		// Because this is now a DM room with exactly one other (joined|invited) member.
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmBobChris, m.MatchRoomAvatar(bob.AvatarURL)))
	})

	t.Run("Bob's avatar change propagates", func(t *testing.T) {
		t.Log("Bob changes his avatar.")
		bobAvatar2 := uploadAvatar(bob, "bob2.png")
		bob.SetAvatar(t, bobAvatar2)

		avatarChangeInDM := false
		avatarChangeInGroupDM := false
		t.Log("Alice syncs until she sees Bob's new avatar.")
		res = alice.SlidingSyncUntil(
			t,
			res.Pos,
			sync3.Request{},
			func(response *sync3.Response) error {
				if !avatarChangeInDM {
					err := m.MatchRoomSubscription(dmBob, m.MatchRoomAvatar(bob.AvatarURL))(response)
					if err == nil {
						avatarChangeInDM = true
					}
				}

				if !avatarChangeInGroupDM {
					err := m.MatchRoomSubscription(dmBobChris, m.MatchRoomAvatar(bob.AvatarURL))(response)
					if err == nil {
						avatarChangeInGroupDM = true
					}
				}

				if avatarChangeInDM && avatarChangeInGroupDM {
					return nil
				}
				return fmt.Errorf("still waiting: avatarChangeInDM=%t avatarChangeInGroupDM=%t", avatarChangeInDM, avatarChangeInGroupDM)
			},
		)

		t.Log("Bob removes his avatar.")
		bob.SetAvatar(t, "")

		avatarChangeInDM = false
		avatarChangeInGroupDM = false
		t.Log("Alice syncs until she sees Bob's avatars vanish.")
		res = alice.SlidingSyncUntil(
			t,
			res.Pos,
			sync3.Request{},
			func(response *sync3.Response) error {
				if !avatarChangeInDM {
					err := m.MatchRoomSubscription(dmBob, m.MatchRoomUnsetAvatar())(response)
					if err == nil {
						avatarChangeInDM = true
					} else {
						t.Log(err)
					}
				}

				if !avatarChangeInGroupDM {
					err := m.MatchRoomSubscription(dmBobChris, m.MatchRoomUnsetAvatar())(response)
					if err == nil {
						avatarChangeInGroupDM = true
					} else {
						t.Log(err)
					}
				}

				if avatarChangeInDM && avatarChangeInGroupDM {
					return nil
				}
				return fmt.Errorf("still waiting: avatarChangeInDM=%t avatarChangeInGroupDM=%t", avatarChangeInDM, avatarChangeInGroupDM)
			},
		)

	})

	t.Run("Explicit avatar propagates in non-DM room", func(t *testing.T) {
		t.Log("Alice sets an avatar for the public room.")
		publicAvatar := uploadAvatar(alice, "public.png")
		alice.Unsafe_SendEventUnsynced(t, public, b.Event{
			Type:     "m.room.avatar",
			StateKey: ptr(""),
			Content: map[string]interface{}{
				"url": publicAvatar,
			},
		})
		t.Log("Alice syncs until she sees that avatar.")
		res = alice.SlidingSyncUntil(
			t,
			res.Pos,
			sync3.Request{},
			m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
				public: {m.MatchRoomAvatar(publicAvatar)},
			}),
		)

		t.Log("Alice changes the avatar for the public room.")
		publicAvatar2 := uploadAvatar(alice, "public2.png")
		alice.Unsafe_SendEventUnsynced(t, public, b.Event{
			Type:     "m.room.avatar",
			StateKey: ptr(""),
			Content: map[string]interface{}{
				"url": publicAvatar2,
			},
		})
		t.Log("Alice syncs until she sees that avatar.")
		res = alice.SlidingSyncUntil(
			t,
			res.Pos,
			sync3.Request{},
			m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
				public: {m.MatchRoomAvatar(publicAvatar2)},
			}),
		)

		t.Log("Alice removes the avatar for the public room.")
		alice.Unsafe_SendEventUnsynced(t, public, b.Event{
			Type:     "m.room.avatar",
			StateKey: ptr(""),
			Content:  map[string]interface{}{},
		})
		t.Log("Alice syncs until she sees that avatar vanish.")
		res = alice.SlidingSyncUntil(
			t,
			res.Pos,
			sync3.Request{},
			m.MatchRoomSubscriptions(map[string][]m.RoomMatcher{
				public: {m.MatchRoomUnsetAvatar()},
			}),
		)
	})

	t.Run("Explicit avatar propagates in DM room", func(t *testing.T) {
		t.Log("Alice re-invites Chris to their DM.")
		alice.InviteRoom(t, dmChris, chris.UserID)

		t.Log("Alice syncs until she sees her invitation to Chris.")
		res = alice.SlidingSyncUntilMembership(t, res.Pos, dmChris, chris, "invite")

		t.Log("Alice should see the DM with Chris's avatar.")
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmChris, m.MatchRoomAvatar(chris.AvatarURL)))

		t.Log("Chris joins the room.")
		chris.JoinRoom(t, dmChris, nil)

		t.Log("Alice syncs until she sees Chris's join.")
		res = alice.SlidingSyncUntilMembership(t, res.Pos, dmChris, chris, "join")

		t.Log("Alice shouldn't see the DM's avatar change..")
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmChris, m.MatchRoomUnchangedAvatar()))

		t.Log("Chris gives their DM a bespoke avatar.")
		dmAvatar := uploadAvatar(chris, "dm.png")
		chris.Unsafe_SendEventUnsynced(t, dmChris, b.Event{
			Type:     "m.room.avatar",
			StateKey: ptr(""),
			Content: map[string]interface{}{
				"url": dmAvatar,
			},
		})

		t.Log("Alice syncs until she sees that avatar.")
		alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(dmChris, m.MatchRoomAvatar(dmAvatar)))

		t.Log("Chris changes his global avatar, which adds a join event to the room.")
		chrisAvatar2 := uploadAvatar(chris, "chris2.png")
		chris.SetAvatar(t, chrisAvatar2)

		t.Log("Alice syncs until she sees that join event.")
		res = alice.SlidingSyncUntilMembership(t, res.Pos, dmChris, chris, "join")

		t.Log("Her response should have either no avatar change, or the same bespoke avatar.")
		// No change, ideally, but repeating the same avatar isn't _wrong_
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmChris, func(r sync3.Room) error {
			noChangeErr := m.MatchRoomUnchangedAvatar()(r)
			sameBespokeAvatarErr := m.MatchRoomAvatar(dmAvatar)(r)
			if noChangeErr == nil || sameBespokeAvatarErr == nil {
				return nil
			}
			return fmt.Errorf("expected no change or the same bespoke avatar (%s), got '%s'", dmAvatar, r.AvatarChange)
		}))

		t.Log("Chris updates the DM's avatar.")
		dmAvatar2 := uploadAvatar(chris, "dm2.png")
		chris.Unsafe_SendEventUnsynced(t, dmChris, b.Event{
			Type:     "m.room.avatar",
			StateKey: ptr(""),
			Content: map[string]interface{}{
				"url": dmAvatar2,
			},
		})

		t.Log("Alice syncs until she sees that avatar.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(dmChris, m.MatchRoomAvatar(dmAvatar2)))

		t.Log("Chris removes the DM's avatar.")
		chris.Unsafe_SendEventUnsynced(t, dmChris, b.Event{
			Type:     "m.room.avatar",
			StateKey: ptr(""),
			Content:  map[string]interface{}{},
		})

		t.Log("Alice syncs until the DM avatar returns to Chris's most recent avatar.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(dmChris, m.MatchRoomAvatar(chris.AvatarURL)))
	})

	t.Run("Changing DM flag", func(t *testing.T) {
		// XXX: the test's expectations might very well be out of date now
		t.Skip("TODO: unimplemented")
		t.Log("Alice clears the DM flag on Bob's room.")
		alice.MustSetGlobalAccountData(t, "m.direct", map[string]interface{}{
			"content": map[string][]string{
				bob.UserID:   {}, // no dmBob here
				chris.UserID: {dmChris, dmBobChris},
			},
		})

		t.Log("Alice syncs until she sees a new set of account data.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{
			Extensions: extensions.Request{
				AccountData: &extensions.AccountDataRequest{
					extensions.Core{Enabled: &boolTrue},
				},
			},
		}, func(response *sync3.Response) error {
			if response.Extensions.AccountData == nil {
				return fmt.Errorf("no account data yet")
			}
			if len(response.Extensions.AccountData.Global) == 0 {
				return fmt.Errorf("no global account data yet")
			}
			return nil
		})

		t.Log("The DM with Bob should no longer be a DM and should no longer have an avatar.")
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmBob, func(r sync3.Room) error {
			if r.IsDM {
				return fmt.Errorf("dmBob is still a DM")
			}
			return m.MatchRoomUnsetAvatar()(r)
		}))

		t.Log("Alice sets the DM flag on Bob's room.")
		alice.MustSetGlobalAccountData(t, "m.direct", map[string]interface{}{
			"content": map[string][]string{
				bob.UserID:   {dmBob}, // dmBob reinstated
				chris.UserID: {dmChris, dmBobChris},
			},
		})

		t.Log("Alice syncs until she sees a new set of account data.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{
			Extensions: extensions.Request{
				AccountData: &extensions.AccountDataRequest{
					extensions.Core{Enabled: &boolTrue},
				},
			},
		}, func(response *sync3.Response) error {
			if response.Extensions.AccountData == nil {
				return fmt.Errorf("no account data yet")
			}
			if len(response.Extensions.AccountData.Global) == 0 {
				return fmt.Errorf("no global account data yet")
			}
			return nil
		})

		t.Log("The room should have Bob's avatar again.")
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmBob, func(r sync3.Room) error {
			if !r.IsDM {
				return fmt.Errorf("dmBob is still not a DM")
			}
			return m.MatchRoomAvatar(bob.AvatarURL)(r)
		}))

	})

	t.Run("See avatar when invited", func(t *testing.T) {
		t.Log("Chris invites Alice to a DM.")
		dmInvited := chris.MustCreateRoom(t, map[string]interface{}{
			"preset":    "trusted_private_chat",
			"is_direct": true,
			"invite":    []string{alice.UserID},
		})

		t.Log("Alice syncs until she sees the invite.")
		res = alice.SlidingSyncUntilMembership(t, res.Pos, dmInvited, alice, "invite")

		// TODO: should alice's client set the DM flag now?

		t.Log("The new room should appear as a DM and use Chris's avatar.")
		m.MatchResponse(t, res, m.MatchRoomSubscription(dmInvited, m.MatchRoomIsDM(true), m.MatchRoomAvatar(chris.AvatarURL)))
	})

	t.Run("Creator of a non-DM never sees an avatar", func(t *testing.T) {
		t.Log("Alice makes a new room which is not a DM.")
		privateGroup := alice.MustCreateRoom(t, map[string]interface{}{
			"preset":    "trusted_private_chat",
			"is_direct": false,
		})

		t.Log("Alice sees the group. It has no avatar.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, m.MatchRoomSubscription(privateGroup, m.MatchRoomUnsetAvatar()))
		m.MatchResponse(t, res, m.MatchRoomSubscription(privateGroup, m.MatchRoomIsDM(false)))

		t.Log("Alice invites Bob to the group, who accepts.")
		alice.MustInviteRoom(t, privateGroup, bob.UserID)
		bob.MustJoinRoom(t, privateGroup, nil)

		t.Log("Alice sees Bob join. The room still has no avatar.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, func(response *sync3.Response) error {
			matchNoAvatarChange := m.MatchRoomSubscription(privateGroup, m.MatchRoomUnchangedAvatar())
			if err := matchNoAvatarChange(response); err != nil {
				t.Fatalf("Saw group avatar change: %s", err)
			}
			matchJoin := m.MatchRoomSubscription(dmBob, MatchRoomTimelineMostRecent(1, []Event{
				{
					Type:     "m.room.member",
					Sender:   bob.UserID,
					StateKey: ptr(bob.UserID),
				},
			}))
			return matchJoin(response)
		})

		t.Log("Alice invites Chris to the group, who accepts.")
		alice.MustInviteRoom(t, privateGroup, chris.UserID)
		chris.MustJoinRoom(t, privateGroup, nil)

		t.Log("Alice sees Chris join. The room still has no avatar.")
		res = alice.SlidingSyncUntil(t, res.Pos, sync3.Request{}, func(response *sync3.Response) error {
			matchNoAvatarChange := m.MatchRoomSubscription(privateGroup, m.MatchRoomUnchangedAvatar())
			if err := matchNoAvatarChange(response); err != nil {
				t.Fatalf("Saw group avatar change: %s", err)
			}
			matchJoin := m.MatchRoomSubscription(dmBob, MatchRoomTimelineMostRecent(1, []Event{
				{
					Type:     "m.room.member",
					Sender:   chris.UserID,
					StateKey: ptr(chris.UserID),
				},
			}))
			return matchJoin(response)
		})

	})
}
