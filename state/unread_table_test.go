package state

import (
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestUnreadTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewUnreadTable(db)
	userID := "@alice:localhost"
	roomA := "!TestUnreadTableA:localhost"
	roomB := "!TestUnreadTableB:localhost"
	roomC := "!TestUnreadTableC:localhost"

	two := 2
	one := 1
	zero := 0

	// try all kinds of insertions
	assertNoError(t, table.UpdateUnreadCounters(userID, roomA, &two, &one)) // both
	assertNoError(t, table.UpdateUnreadCounters(userID, roomB, &two, nil))  // one
	assertNoError(t, table.UpdateUnreadCounters(userID, roomC, nil, &two))  // one
	assertUnread(t, table, userID, roomA, 2, 1)
	assertUnread(t, table, userID, roomB, 2, 0)
	assertUnread(t, table, userID, roomC, 0, 2)

	// try all kinds of updates
	assertNoError(t, table.UpdateUnreadCounters(userID, roomA, &zero, nil)) // one
	assertNoError(t, table.UpdateUnreadCounters(userID, roomB, nil, &two))  // one
	assertNoError(t, table.UpdateUnreadCounters(userID, roomC, &two, &two)) // both
	assertUnread(t, table, userID, roomA, 0, 1)
	assertUnread(t, table, userID, roomB, 2, 2)
	assertUnread(t, table, userID, roomC, 2, 2)
}

func assertUnread(t *testing.T, table *UnreadTable, userID, roomID string, wantHighight, wantNotif int) {
	t.Helper()
	gotHighlight, gotNotif, err := table.SelectUnreadCounters(userID, roomID)
	if err != nil {
		t.Fatalf("SelectUnreadCounters %s %s: %s", userID, roomID, err)
	}
	if gotHighlight != wantHighight {
		t.Errorf("SelectUnreadCounters: got %d highlights, want %d", gotHighlight, wantHighight)
	}
	if gotNotif != wantNotif {
		t.Errorf("SelectUnreadCounters: got %d notifs, want %d", gotNotif, wantNotif)
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("got error: %s", err)
	}
}
