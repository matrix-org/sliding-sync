package handler

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/matrix-org/sync-v3/sync3"
)

func TestRoomsBuilder(t *testing.T) {
	sortBuiltSubs := func(bs []BuiltSubscription) []BuiltSubscription {
		for i := range bs {
			sort.Strings(bs[i].RoomIDs)
			sort.Slice(bs[i].RoomSubscription.RequiredState, func(x, y int) bool {
				a := bs[i].RoomSubscription.RequiredState[x]
				b := bs[i].RoomSubscription.RequiredState[y]
				return strings.Join(a[:], " ") < strings.Join(b[:], " ")
			})
		}
		sort.Slice(bs, func(i, j int) bool {
			return strings.Join(bs[i].RoomIDs, " ") < strings.Join(bs[j].RoomIDs, " ")
		})
		return bs
	}
	testCases := []struct {
		name      string
		subsToAdd []BuiltSubscription // reuse builtsubscription
		want      []BuiltSubscription
	}{
		{
			name: "single subscription",
			subsToAdd: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.topic", ""}},
						TimelineLimit: 15,
					},
					RoomIDs: []string{"!a", "!b", "!c"},
				},
			},
			want: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.topic", ""}},
						TimelineLimit: 15,
					},
					RoomIDs: []string{"!a", "!b", "!c"},
				},
			},
		},
		{
			name: "2 non-overlapping subscriptions",
			subsToAdd: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.topic", ""}},
						TimelineLimit: 15,
					},
					RoomIDs: []string{"!a", "!b", "!c"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!d", "!e", "!f"},
				},
			},
			want: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.topic", ""}},
						TimelineLimit: 15,
					},
					RoomIDs: []string{"!a", "!b", "!c"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!d", "!e", "!f"},
				},
			},
		},
		{
			name: "partial overlapping subscriptions",
			subsToAdd: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.topic", ""}},
						TimelineLimit: 15,
					},
					RoomIDs: []string{"!a", "!b", "!c"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!b", "!c", "!d"},
				},
			},
			want: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.topic", ""}},
						TimelineLimit: 15,
					},
					RoomIDs: []string{"!a"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!d"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}, {"m.room.topic", ""}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!b", "!c"},
				},
			},
		},
		{
			name: "3 list example",
			subsToAdd: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}, {"m.room.topic", ""}},
						TimelineLimit: 1,
					},
					RoomIDs: []string{"!a", "!b", "!c"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}},
						TimelineLimit: 1,
					},
					RoomIDs: []string{"!b", "!c", "!d"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"*", "*"}},
						TimelineLimit: 50,
					},
					RoomIDs: []string{"!a"},
				},
			},
			want: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"*", "*"}, {"m.room.name", ""}, {"m.room.topic", ""}},
						TimelineLimit: 50,
					},
					RoomIDs: []string{"!a"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}, {"m.room.name", ""}, {"m.room.topic", ""}},
						TimelineLimit: 1,
					},
					RoomIDs: []string{"!b", "!c"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"m.room.name", ""}},
						TimelineLimit: 1,
					},
					RoomIDs: []string{"!d"},
				},
			},
		},
		{
			name: "3-circle venn diagram",
			subsToAdd: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"a", "a"}},
						TimelineLimit: 5,
					},
					RoomIDs: []string{"!abc", "!a", "!ab", "!ac"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"b", "b"}},
						TimelineLimit: 10,
					},
					RoomIDs: []string{"!abc", "!b", "!ab", "!bc"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"c", "c"}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!abc", "!c", "!ac", "!bc"},
				},
			},
			want: []BuiltSubscription{
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"a", "a"}},
						TimelineLimit: 5,
					},
					RoomIDs: []string{"!a"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"b", "b"}},
						TimelineLimit: 10,
					},
					RoomIDs: []string{"!b"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"c", "c"}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!c"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"a", "a"}, {"b", "b"}},
						TimelineLimit: 10,
					},
					RoomIDs: []string{"!ab"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"a", "a"}, {"c", "c"}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!ac"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"c", "c"}, {"b", "b"}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!bc"},
				},
				{
					RoomSubscription: sync3.RoomSubscription{
						RequiredState: [][2]string{{"a", "a"}, {"b", "b"}, {"c", "c"}},
						TimelineLimit: 20,
					},
					RoomIDs: []string{"!abc"},
				},
			},
		},
	}
	for _, tc := range testCases {
		rb := NewRoomsBuilder()
		for _, bs := range tc.subsToAdd {
			id := rb.AddSubscription(bs.RoomSubscription)
			rb.AddRoomsToSubscription(id, bs.RoomIDs)
		}
		got := rb.BuildSubscriptions()
		tc.want = sortBuiltSubs(tc.want)
		got = sortBuiltSubs(got)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: \ngot  %+v\n want %+v", tc.name, got, tc.want)
		}
	}
}
