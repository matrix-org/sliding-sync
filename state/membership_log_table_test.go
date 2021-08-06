package state

import (
	"reflect"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestMembershipLogTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	tx, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer tx.Commit()

	// Test inserting membership logs
	roomID := "!TestMembershipLogTable:localhost"
	targetUser := "@TestMembershipLogTable:localhost"
	entries := []struct {
		roomID     string
		eventNID   int64
		targetUser string
	}{
		{
			eventNID:   101,
			roomID:     roomID,
			targetUser: targetUser,
		},
		{
			eventNID:   103,
			roomID:     "a different room",
			targetUser: targetUser,
		},
		{
			eventNID:   110,
			roomID:     roomID,
			targetUser: "@a-different-user:localhost",
		},
		{
			eventNID:   111,
			roomID:     "different room again",
			targetUser: "@a-different-user-again:localhost",
		},
		{
			eventNID:   115,
			roomID:     roomID,
			targetUser: targetUser,
		},
		{
			eventNID:   119,
			roomID:     roomID,
			targetUser: targetUser,
		},
		// duplicate ok
		{
			eventNID:   119,
			roomID:     roomID,
			targetUser: targetUser,
		},
	}
	table := NewMembershipLogTable(db)
	for _, entry := range entries {
		if err := table.AppendMembership(tx, entry.eventNID, entry.roomID, entry.targetUser); err != nil {
			t.Fatalf("failed to AppendMembership: %s", err)
		}
	}

	// Test querying the membership log
	testCases := []struct {
		startExcl int64
		endIncl   int64
		target    string
		wantNIDs  []int64
	}{
		// entire table
		{
			startExcl: MembershipLogOffsetStart,
			endIncl:   999,
			target:    targetUser,
			wantNIDs:  []int64{101, 103, 115, 119},
		},
		// middle entries (no results)
		{
			startExcl: 103,
			endIncl:   111,
			target:    targetUser,
			wantNIDs:  nil,
		},
		// last entry
		{
			startExcl: 117,
			endIncl:   119,
			target:    targetUser,
			wantNIDs:  []int64{119},
		},
		// random user
		{
			startExcl: MembershipLogOffsetStart,
			endIncl:   999,
			target:    "a-random-user",
			wantNIDs:  nil,
		},
	}
	for _, tc := range testCases {
		gotNIDs, err := table.MembershipsBetween(tx, tc.startExcl, tc.endIncl, tc.target)
		if err != nil {
			t.Errorf("MembershipsBetween %+v returned error: %s", tc, err)
			continue
		}
		if len(gotNIDs) != len(tc.wantNIDs) {
			t.Errorf("MembershipsBetween %+v: returned wrong number of events nids, got %v want %v", tc, gotNIDs, tc.wantNIDs)
			continue
		}
		if !reflect.DeepEqual(gotNIDs, tc.wantNIDs) {
			t.Errorf("MembershipsBetween %+v: returned wrong event nids, got %v want %v", tc, gotNIDs, tc.wantNIDs)
		}
	}
	// test querying by room ID
	roomTestCases := []struct {
		startExcl int64
		endIncl   int64
		limit     int64
		roomID    string
		wantNIDs  []int64
	}{
		// entire table
		{
			startExcl: MembershipLogOffsetStart,
			endIncl:   9999,
			roomID:    roomID,
			limit:     999,
			wantNIDs:  []int64{101, 110, 115, 119},
		},
		// hitting the limit
		{
			startExcl: 101,
			endIncl:   9999,
			roomID:    roomID,
			limit:     2,
			wantNIDs:  []int64{110, 115},
		},

		// limit matches all responses
		{
			startExcl: 110,
			endIncl:   9999,
			roomID:    roomID,
			limit:     2,
			wantNIDs:  []int64{115, 119},
		},
	}
	for _, tc := range roomTestCases {
		gotNIDs, err := table.MembershipsBetweenForRoom(tx, tc.startExcl, tc.endIncl, tc.limit, tc.roomID)
		if err != nil {
			t.Errorf("MembershipsBetweenForRoom %+v returned error: %s", tc, err)
			continue
		}
		if len(gotNIDs) != len(tc.wantNIDs) {
			t.Errorf("MembershipsBetweenForRoom %+v: returned wrong number of events nids, got %v want %v", tc, gotNIDs, tc.wantNIDs)
			continue
		}
		if !reflect.DeepEqual(gotNIDs, tc.wantNIDs) {
			t.Errorf("MembershipsBetweenForRoom %+v: returned wrong event nids, got %v want %v", tc, gotNIDs, tc.wantNIDs)
		}
	}

	// Test deleting membership logs
	numDeleted, err := table.DeleteLogs(tx, 101, 119, roomID)
	if err != nil {
		t.Fatalf("failed to DeleteLogs: %s", err)
	}
	if numDeleted != 3 {
		t.Errorf("DeleteLogs did not delete right number of events, got %d want 3", numDeleted)
	}

}
