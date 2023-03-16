package syncv3_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

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
		unencryptedRoomID := alice.CreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
		})
		unencryptedRoomIDs = append([]string{unencryptedRoomID}, unencryptedRoomIDs...) // push in array
		encryptedRoomID := alice.CreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
			"initial_state": []Event{
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
		groupRoomID := alice.CreateRoom(t, map[string]interface{}{
			"preset": "public_chat",
		})
		groupRoomIDs = append([]string{groupRoomID}, groupRoomIDs...) // push in array
		dmRoomID := alice.CreateRoom(t, map[string]interface{}{
			"preset":    "trusted_private_chat",
			"is_direct": true,
			"invite":    []string{dmUserID},
		})
		dmRoomIDs = append([]string{dmRoomID}, dmRoomIDs...) // push in array
		dmContent[dmUserID] = []string{dmRoomID}
		time.Sleep(time.Millisecond) // ensure timestamp changes
	}
	// set the account data
	alice.SetGlobalAccountData(t, "m.direct", dmContent)

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
	pingEventID := alice.SendEventSynced(t, dmRoomIDs[len(dmRoomIDs)-1], Event{
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
		roomID := alice.CreateRoom(t, map[string]interface{}{
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
			createContent["initial_state"] = []Event{
				NewEncryptionEvent(),
			}
		}
		if isDM {
			createContent["is_direct"] = true
			createContent["invite"] = []string{dmUserID}
		}
		roomID := alice.CreateRoom(t, createContent)
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
	alice.SetGlobalAccountData(t, "m.direct", dmContent)

	// seed the proxy: so we can get timeline correctly as it uses limit:1 initially.
	alice.SlidingSync(t, sync3.Request{})

	// send messages to track timeline. The most recent messages are:
	// - ENCRYPTION EVENT (if set)
	// - DM INVITE EVENT (if set)
	// - This ping message (always)
	roomToEventID := make(map[string]string, len(allRoomIDs))
	for i := len(allRoomIDs) - 1; i >= 0; i-- {
		roomToEventID[allRoomIDs[i]] = alice.SendEventSynced(t, allRoomIDs[i], Event{
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
				MatchRoomRequiredState([]Event{
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
				MatchRoomRequiredState([]Event{
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
				MatchRoomRequiredState([]Event{
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
				MatchRoomRequiredState([]Event{
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
				MatchRoomRequiredState([]Event{
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
				MatchRoomRequiredState([]Event{
					{
						Type:     "m.room.power_levels",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimelineMostRecent(1, []Event{{ID: roomToEventID[dmRoomIDs[2]]}}),
			},
			dmRoomIDs[3]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredState([]Event{
					{
						Type:     "m.room.power_levels",
						StateKey: ptr(""),
					},
				}),
				MatchRoomTimelineMostRecent(1, []Event{{ID: roomToEventID[dmRoomIDs[3]]}}),
			},
			dmRoomIDs[4]: {
				m.MatchRoomInitial(true),
				MatchRoomRequiredState([]Event{
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
	alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
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
	alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat"})
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
				roomID := alice.CreateRoom(t, map[string]interface{}{"preset": "public_chat", "name": roomName})
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
		roomIDs[i] = alice.CreateRoom(t, map[string]interface{}{
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
		m.MatchV3InvalidateOp(0, 20),
		m.MatchV3SyncOp(0, 20, []string{gotNameToIDs["Apple"], gotNameToIDs["Kiwi"], gotNameToIDs["Lemon"], gotNameToIDs["Orange"]}),
	)))
}

// Regression test that a window can be shrunk and the INVALIDATE appears correctly for the low/high ends of the range
func TestShrinkRange(t *testing.T) {
	alice := registerNewUser(t)
	var roomIDs []string // most recent first
	for i := 0; i < 10; i++ {
		time.Sleep(time.Millisecond) // ensure creation timestamp changes
		roomIDs = append([]string{alice.CreateRoom(t, map[string]interface{}{
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
		m.MatchV3SyncOp(0, 20, roomIDs),
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
		m.MatchV3InvalidateOp(7, 20),
	)))
}

// Regression test that you can expand a window without it causing problems. Previously, it would cause
// a spurious {"op":"SYNC","range":[11,20],"room_ids":["!jHVHDxEWqTIcbDYoah:synapse"]} op for a bad index
func TestExpandRange(t *testing.T) {
	alice := registerNewUser(t)
	var roomIDs []string // most recent first
	for i := 0; i < 10; i++ {
		time.Sleep(time.Millisecond) // ensure creation timestamp changes
		roomIDs = append([]string{alice.CreateRoom(t, map[string]interface{}{
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
		m.MatchV3SyncOp(0, 10, roomIDs),
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
		roomIDs = append([]string{alice.CreateRoom(t, map[string]interface{}{
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
			m.MatchV3SyncOp(0, 20, roomIDs, false),
		)),
		m.MatchList("2", m.MatchV3Count(16), m.MatchV3Ops(
			m.MatchV3SyncOp(0, 16, roomIDs, false),
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
			m.MatchV3InvalidateOp(16, 20),
		)),
		m.MatchList("2", m.MatchV3Count(16), m.MatchV3Ops()),
	)
}

// NB: assumes bump_event_types is sticky
func TestBumpEventTypesHandling(t *testing.T) {
	alice := registerNewUser(t)
	bob := registerNewUser(t)
	charlie := registerNewUser(t)

	t.Log("Alice creates two rooms")
	room1 := alice.CreateRoom(
		t,
		map[string]interface{}{
			"preset": "public_chat",
			"name":   "room1",
		},
	)
	room2 := alice.CreateRoom(
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
	bob.SendEventSynced(t, room2, Event{
		Type: "m.room.message",
		Content: map[string]interface{}{
			"body":    "Hi room 2",
			"msgtype": "m.text",
		},
	})
	bob.SendEventSynced(t, room1, Event{
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
	}
	aliceSyncRequest := sync3.Request{
		Lists: map[string]sync3.RequestList{
			"room_list": aliceReqList,
		},
		BumpEventTypes: []string{"m.room.message", "m.room.encrypted"},
	}
	// TODO: to avoid flakes, want this sync to include/cover Bob's messages in both rooms.
	aliceRes := alice.SlidingSync(t, aliceSyncRequest)

	t.Log("Alice's sync response should include room1 ahead of room 2.")
	matchRoom1ThenRoom2 := m.MatchList("room_list",
		m.MatchV3Count(2),
		m.MatchV3Ops(
			m.MatchV3SyncOp(0, 20, []string{room1, room2}, false),
		))
	m.MatchResponse(t, aliceRes, matchRoom1ThenRoom2)

	t.Log("Bob requests a sliding sync that bumps rooms on messages and memberships.")
	bobReqList := sync3.RequestList{
		Sort:   []string{sync3.SortByRecency, sync3.SortByName},
		Ranges: sync3.SliceRanges{{0, 20}},
		RoomSubscription: sync3.RoomSubscription{
			RequiredState: [][2]string{{"m.room.avatar", ""}, {"m.room.encryption", ""}},
			TimelineLimit: 10,
		},
	}
	bobSyncRequest := sync3.Request{
		Lists: map[string]sync3.RequestList{
			"room_list": bobReqList,
		},
		BumpEventTypes: []string{"m.room.message", "m.room.encrypted", "m.room.member"},
	}
	bobRes := bob.SlidingSync(t, bobSyncRequest)

	t.Log("Bob should also see room 1 ahead of room 2 in his sliding sync response.")
	m.MatchResponse(t, bobRes, matchRoom1ThenRoom2)

	t.Log("Charlie joins room 2.")
	charlie.JoinRoom(t, room2, nil)

	t.Log("Alice syncs until she sees Charlie's membership.")
	aliceRes = alice.SlidingSyncUntilMembership(t, aliceRes.Pos, room2, charlie, "join")

	t.Log("Alice shouldn't see any rooms' positions change.")
	matchNoOps := m.MatchList("room_list",
		m.MatchV3Count(2),
		m.MatchV3Ops(),
	)
	m.MatchResponse(t, aliceRes, matchNoOps)

	t.Log("Bob syncs until he sees Charlie's membership.")
	bobRes = bob.SlidingSyncUntilMembership(t, bobRes.Pos, room2, charlie, "join")

	t.Log("Bob should see room 2 at the top of his list.")
	matchBobSeesRoom2Bumped := m.MatchList("room_list",
		m.MatchV3Count(2),
		m.MatchV3Ops(
			m.MatchV3DeleteOp(1),
			m.MatchV3InsertOp(0, room2),
		),
	)

	m.MatchResponse(t, bobRes, matchBobSeesRoom2Bumped)
}

// Test joining a room puts it at the top of your list. What about invites?
