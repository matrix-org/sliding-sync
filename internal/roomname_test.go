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
