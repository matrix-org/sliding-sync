package state

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
)

func sortReceipts(receipts []internal.Receipt) {
	sort.Slice(receipts, func(i, j int) bool {
		keyi := receipts[i].EventID + receipts[i].RoomID + receipts[i].UserID + receipts[i].ThreadID
		keyj := receipts[j].EventID + receipts[j].RoomID + receipts[j].UserID + receipts[j].ThreadID
		return keyi < keyj
	})
}

func parsedReceiptsEqual(t *testing.T, got, want []internal.Receipt) {
	t.Helper()
	sortReceipts(got)
	sortReceipts(want)
	if len(got) != len(want) {
		t.Fatalf("got %d, want %d, got: %+v want %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("i=%d got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestReceiptPacking(t *testing.T) {
	testCases := []struct {
		receipts []internal.Receipt
		wantEDU  receiptEDU
		name     string
	}{
		{
			name: "single receipt",
			receipts: []internal.Receipt{
				{
					RoomID:  "!foo",
					EventID: "$bar",
					UserID:  "@baz",
					TS:      42,
				},
			},
			wantEDU: receiptEDU{
				Type: "m.receipt",
				Content: map[string]receiptContent{
					"$bar": {
						Read: map[string]receiptInfo{
							"@baz": {
								TS: 42,
							},
						},
					},
				},
			},
		},
		{
			name: "two distinct receipt",
			receipts: []internal.Receipt{
				{
					RoomID:  "!foo",
					EventID: "$bar",
					UserID:  "@baz",
					TS:      42,
				},
				{
					RoomID:  "!foo2",
					EventID: "$bar2",
					UserID:  "@baz2",
					TS:      422,
				},
			},
			wantEDU: receiptEDU{
				Type: "m.receipt",
				Content: map[string]receiptContent{
					"$bar": {
						Read: map[string]receiptInfo{
							"@baz": {
								TS: 42,
							},
						},
					},
					"$bar2": {
						Read: map[string]receiptInfo{
							"@baz2": {
								TS: 422,
							},
						},
					},
				},
			},
		},
		{
			name: "MSC4102: unthreaded wins when threaded first",
			receipts: []internal.Receipt{
				{
					RoomID:   "!foo",
					EventID:  "$bar",
					UserID:   "@baz",
					TS:       42,
					ThreadID: "thread_id",
				},
				{
					RoomID:  "!foo",
					EventID: "$bar",
					UserID:  "@baz",
					TS:      420,
				},
			},
			wantEDU: receiptEDU{
				Type: "m.receipt",
				Content: map[string]receiptContent{
					"$bar": {
						Read: map[string]receiptInfo{
							"@baz": {
								TS: 420,
							},
						},
					},
				},
			},
		},
		{
			name: "MSC4102: unthreaded wins when unthreaded first",
			receipts: []internal.Receipt{
				{
					RoomID:  "!foo",
					EventID: "$bar",
					UserID:  "@baz",
					TS:      420,
				},
				{
					RoomID:   "!foo",
					EventID:  "$bar",
					UserID:   "@baz",
					TS:       42,
					ThreadID: "thread_id",
				},
			},
			wantEDU: receiptEDU{
				Type: "m.receipt",
				Content: map[string]receiptContent{
					"$bar": {
						Read: map[string]receiptInfo{
							"@baz": {
								TS: 420,
							},
						},
					},
				},
			},
		},
		{
			name: "MSC4102: unthreaded wins in private receipts when unthreaded first",
			receipts: []internal.Receipt{
				{
					RoomID:    "!foo",
					EventID:   "$bar",
					UserID:    "@baz",
					TS:        420,
					IsPrivate: true,
				},
				{
					RoomID:    "!foo",
					EventID:   "$bar",
					UserID:    "@baz",
					TS:        42,
					ThreadID:  "thread_id",
					IsPrivate: true,
				},
			},
			wantEDU: receiptEDU{
				Type: "m.receipt",
				Content: map[string]receiptContent{
					"$bar": {
						ReadPrivate: map[string]receiptInfo{
							"@baz": {
								TS: 420,
							},
						},
					},
				},
			},
		},
	}
	for _, tc := range testCases {
		edu, err := PackReceiptsIntoEDU(tc.receipts)
		if err != nil {
			t.Fatalf("%s: PackReceiptsIntoEDU: %s", tc.name, err)
		}
		gotEDU := receiptEDU{
			Type:    "m.receipt",
			Content: make(map[string]receiptContent),
		}
		if err := json.Unmarshal(edu, &gotEDU); err != nil {
			t.Fatalf("%s: json.Unmarshal: %s", tc.name, err)
		}
		if !reflect.DeepEqual(gotEDU, tc.wantEDU) {
			t.Errorf("%s: EDU mismatch, got  %+v\n want %+v", tc.name, gotEDU, tc.wantEDU)
		}
	}
}

func TestReceiptTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	roomA := "!A:ReceiptTable"
	roomB := "!B:ReceiptTable"
	edu := json.RawMessage(`{
		"content": {
		  "$1435641916114394fHBLK:matrix.org": {
			"m.read": {
			  "@rikj:jki.re": {
				"ts": 1436451550453
			  }
			},
			"m.read.private": {
			  "@self:example.org": {
				"ts": 1661384801651
			  }
			}
		  }
		},
		"type": "m.receipt"
	  }`)
	table := NewReceiptTable(db)

	// inserting same receipts for different rooms should work - compound key should include the room ID
	for _, roomID := range []string{roomA, roomB} {
		newReceipts, err := table.Insert(roomID, edu)
		if err != nil {
			t.Fatalf("Insert: %s", err)
		}
		parsedReceiptsEqual(t, newReceipts, []internal.Receipt{
			{
				RoomID:   roomID,
				EventID:  "$1435641916114394fHBLK:matrix.org",
				UserID:   "@rikj:jki.re",
				TS:       1436451550453,
				ThreadID: "",
			},
			{
				RoomID:    roomID,
				EventID:   "$1435641916114394fHBLK:matrix.org",
				UserID:    "@self:example.org",
				TS:        1661384801651,
				ThreadID:  "",
				IsPrivate: true,
			},
		})
	}
	// dupe receipts = no delta
	newReceipts, err := table.Insert(roomA, edu)
	assertNoError(t, err)
	parsedReceiptsEqual(t, newReceipts, nil)

	// selecting receipts -> ignores private receipt
	got, err := table.SelectReceiptsForEvents(roomA, []string{"$1435641916114394fHBLK:matrix.org"})
	assertNoError(t, err)
	parsedReceiptsEqual(t, got, []internal.Receipt{
		{
			RoomID:   roomA,
			EventID:  "$1435641916114394fHBLK:matrix.org",
			UserID:   "@rikj:jki.re",
			TS:       1436451550453,
			ThreadID: "",
		},
	})

	// new receipt with old receipt -> 1 delta, also check thread_id is saved.
	newReceipts, err = table.Insert(roomA, json.RawMessage(`{
		"content": {
		  "$1435641916114394fHBLK:matrix.org": {
			"m.read": {
			  "@rikj:jki.re": {
				"ts": 1436451550453
			  },
			  "@alice:bar": {
				"ts": 123456,
				"thread_id": "yep"
			  }
			}
		  }
		},
		"type": "m.receipt"
	  }`))
	assertNoError(t, err)
	parsedReceiptsEqual(t, newReceipts, []internal.Receipt{
		{
			RoomID:   roomA,
			EventID:  "$1435641916114394fHBLK:matrix.org",
			UserID:   "@alice:bar",
			TS:       123456,
			ThreadID: "yep",
		},
	})

	// updated receipt for user -> 1 delta
	newReceipts, err = table.Insert(roomA, json.RawMessage(`{
			"content": {
			  "$aaaaaaaa:matrix.org": {
				"m.read": {
				  "@rikj:jki.re": {
					"ts": 1436499990453
				  }
				}
			  }
			},
			"type": "m.receipt"
		  }`))
	assertNoError(t, err)
	parsedReceiptsEqual(t, newReceipts, []internal.Receipt{
		{
			RoomID:   roomA,
			EventID:  "$aaaaaaaa:matrix.org",
			UserID:   "@rikj:jki.re",
			TS:       1436499990453,
			ThreadID: "",
		},
	})

	// selecting multiple receipts
	table.Insert(roomA, json.RawMessage(`{
		"content": {
		  "$aaaaaaaa:matrix.org": {
			"m.read": {
				"@bob:bar": {
					"ts": 5555
				},
				"@self:example.org": {
					"ts": 6666,
					"thread_id": "yup"
				}
			}
		  }
		},
		"type": "m.receipt"
	  }`))
	got, err = table.SelectReceiptsForEvents(roomA, []string{"$aaaaaaaa:matrix.org"})
	assertNoError(t, err)
	parsedReceiptsEqual(t, got, []internal.Receipt{
		{
			RoomID:   roomA,
			EventID:  "$aaaaaaaa:matrix.org",
			UserID:   "@rikj:jki.re",
			TS:       1436499990453,
			ThreadID: "",
		},
		{
			RoomID:   roomA,
			EventID:  "$aaaaaaaa:matrix.org",
			UserID:   "@bob:bar",
			TS:       5555,
			ThreadID: "",
		},
		{
			RoomID:   roomA,
			EventID:  "$aaaaaaaa:matrix.org",
			UserID:   "@self:example.org",
			TS:       6666,
			ThreadID: "yup",
		},
	})

	gotMap, err := table.SelectReceiptsForUser([]string{roomA}, "@self:example.org")
	assertNoError(t, err)
	parsedReceiptsEqual(t, gotMap[roomA], []internal.Receipt{
		{
			RoomID:    roomA,
			EventID:   "$1435641916114394fHBLK:matrix.org",
			UserID:    "@self:example.org",
			TS:        1661384801651,
			ThreadID:  "",
			IsPrivate: true,
		},
		{
			RoomID:   roomA,
			EventID:  "$aaaaaaaa:matrix.org",
			UserID:   "@self:example.org",
			TS:       6666,
			ThreadID: "yup",
		},
	})
}
