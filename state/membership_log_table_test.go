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
	roomID := "!foo:bar"
	targetUser := "@alice:localhost"
	entries := []struct {
		roomID     string
		eventNID   int64
		targetUser string
	}{
		{
			eventNID:   1,
			roomID:     roomID,
			targetUser: targetUser,
		},
		{
			eventNID:   3,
			roomID:     "a different room",
			targetUser: targetUser,
		},
		{
			eventNID:   10,
			roomID:     roomID,
			targetUser: "@a-different-user:localhost",
		},
		{
			eventNID:   11,
			roomID:     "different room again",
			targetUser: "@a-different-user-again:localhost",
		},
		{
			eventNID:   15,
			roomID:     roomID,
			targetUser: targetUser,
		},
		{
			eventNID:   19,
			roomID:     roomID,
			targetUser: targetUser,
		},
		// duplicate ok
		{
			eventNID:   19,
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
			wantNIDs:  []int64{1, 3, 15, 19},
		},
		// middle entries (no results)
		{
			startExcl: 3,
			endIncl:   11,
			target:    targetUser,
			wantNIDs:  nil,
		},
		// last entry
		{
			startExcl: 17,
			endIncl:   19,
			target:    targetUser,
			wantNIDs:  []int64{19},
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

	// Test deleting membership logs
	numDeleted, err := table.DeleteLogs(tx, 1, 19, roomID)
	if err != nil {
		t.Fatalf("failed to DeleteLogs: %s", err)
	}
	if numDeleted != 3 {
		t.Errorf("DeleteLogs did not delete right number of events, got %d want 3", numDeleted)
	}

}
