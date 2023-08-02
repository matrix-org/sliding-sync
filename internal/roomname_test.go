package internal

import "testing"

func TestCalculateRoomName(t *testing.T) {
	testCases := []struct {
		roomName           string
		canonicalAlias     string
		heroes             []Hero
		joinedCount        int
		invitedCount       int
		maxNumNamesPerRoom int

		wantRoomName string
	}{
		// Room name takes precedence
		{
			roomName:           "My Room Name",
			canonicalAlias:     "#alias:localhost",
			joinedCount:        5,
			invitedCount:       1,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Bob",
				},
			},
			wantRoomName: "My Room Name",
		},
		// Alias takes precedence if room name is missing
		{
			canonicalAlias:     "#alias:localhost",
			joinedCount:        5,
			invitedCount:       1,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Bob",
				},
			},
			wantRoomName: "#alias:localhost",
		},
		// ... and N others (large group chat)
		{
			joinedCount:        5,
			invitedCount:       1,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Bob",
				},
			},
			wantRoomName: "Alice, Bob and 3 others",
		},
		// Small group chat
		{
			joinedCount:        4,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Bob",
				},
				{
					ID:   "@charlie:localhost",
					Name: "Charlie",
				},
			},
			wantRoomName: "Alice, Bob and Charlie",
		},
		// DM room
		{
			joinedCount:        2,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
			},
			wantRoomName: "Alice",
		},
		// 3-way room
		{
			joinedCount:        3,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Bob",
				},
			},
			wantRoomName: "Alice and Bob",
		},
		// 3-way room, one person invited with no display name
		{
			joinedCount:        2,
			invitedCount:       1,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID: "@bob:localhost",
				},
			},
			wantRoomName: "Alice and @bob:localhost",
		},
		// 3-way room, no display names
		{
			joinedCount:        2,
			invitedCount:       1,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID: "@alice:localhost",
				},
				{
					ID: "@bob:localhost",
				},
			},
			wantRoomName: "@alice:localhost and @bob:localhost",
		},
		// disambiguation all
		{
			joinedCount:        10,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Alice",
				},
				{
					ID:   "@charlie:localhost",
					Name: "Alice",
				},
			},
			wantRoomName: "Alice (@alice:localhost), Alice (@bob:localhost), Alice (@charlie:localhost) and 6 others",
		},
		// disambiguation some
		{
			joinedCount:        10,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Bob",
				},
				{
					ID:   "@charlie:localhost",
					Name: "Alice",
				},
			},
			wantRoomName: "Alice (@alice:localhost), Bob, Alice (@charlie:localhost) and 6 others",
		},
		// disambiguation, faking user IDs as display names
		{
			joinedCount:        3,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@evil:localhost",
					Name: "@alice:localhost",
				},
				{
					ID: "@alice:localhost",
				},
			},
			wantRoomName: "@alice:localhost (@evil:localhost) and @alice:localhost (@alice:localhost)",
		},
		// left room
		{
			joinedCount:        1,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes: []Hero{
				{
					ID:   "@alice:localhost",
					Name: "Alice",
				},
				{
					ID:   "@bob:localhost",
					Name: "Bob",
				},
			},
			wantRoomName: "Empty Room (was Alice and Bob)",
		},
		// empty room
		{
			joinedCount:        1,
			invitedCount:       0,
			maxNumNamesPerRoom: 3,
			heroes:             []Hero{},
			wantRoomName:       "Empty Room",
		},
	}

	for _, tc := range testCases {
		gotName := CalculateRoomName(&RoomMetadata{
			NameEvent:      tc.roomName,
			CanonicalAlias: tc.canonicalAlias,
			Heroes:         tc.heroes,
			JoinCount:      tc.joinedCount,
			InviteCount:    tc.invitedCount,
		}, tc.maxNumNamesPerRoom)
		if gotName != tc.wantRoomName {
			t.Errorf("got %s want %s for test case: %+v", gotName, tc.wantRoomName, tc)
		}
	}
}

func TestCopyHeroes(t *testing.T) {
	const alice = "@alice:test"
	const bob = "@bob:test"
	const chris = "@chris:test"
	m1 := RoomMetadata{Heroes: []Hero{
		{ID: alice},
		{ID: bob},
		{ID: chris},
	}}

	m2 := m1.CopyHeroes()
	// Uncomment this to see why CopyHeroes is necessary!
	//m2 := m1

	t.Logf("Compare heroes:\n\tm1=%v\n\tm2=%v", m1.Heroes, m2.Heroes)

	t.Log("Remove chris from m1")
	m1.RemoveHero(chris)
	t.Logf("Compare heroes:\n\tm1=%v\n\tm2=%v", m1.Heroes, m2.Heroes)

	assertSliceIDs(t, "m1.Heroes", m1.Heroes, []string{alice, bob})
	assertSliceIDs(t, "m2.Heroes", m2.Heroes, []string{alice, bob, chris})

	t.Log("Remove alice from m1")
	m1.RemoveHero(alice)
	t.Logf("Compare heroes:\n\tm1=%v\n\tm2=%v", m1.Heroes, m2.Heroes)

	assertSliceIDs(t, "m1.Heroes", m1.Heroes, []string{bob})
	assertSliceIDs(t, "m2.Heroes", m2.Heroes, []string{alice, bob, chris})
}

func assertSliceIDs(t *testing.T, desc string, h []Hero, ids []string) {
	if len(h) != len(ids) {
		t.Errorf("%s has length %d, expected %d", desc, len(h), len(ids))
	}
	for index, id := range ids {
		if h[index].ID != id {
			t.Errorf("%s[%d] ID is %s, expected %s", desc, index, h[index].ID, id)
		}
	}
}
