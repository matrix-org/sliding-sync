package state

import (
	"encoding/json"
	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"reflect"
	"testing"
)

func TestInviteTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewInvitesTable(db)
	alice := "@alice:localhost"
	bob := "@bob:localhost"
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	inviteStateA := []json.RawMessage{[]byte(`{"foo":"bar"}`)}
	inviteStateB := []json.RawMessage{[]byte(`{"foo":"bar"}`), []byte(`{"baz":"quuz"}`)}

	// Add some invites
	if err := table.InsertInvite(alice, roomA, inviteStateA); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}
	if err := table.InsertInvite(bob, roomA, inviteStateB); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}
	if err := table.InsertInvite(alice, roomB, inviteStateB); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}

	// Assert alice's invites (multiple)
	invites, err := table.SelectAllInvitesForUser(alice)
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if len(invites) != 2 {
		t.Fatalf("got %d invites, want 2", len(invites))
	}
	if !reflect.DeepEqual(invites[roomA], inviteStateA) {
		t.Errorf("room %s got %s want %s", roomA, jsonArrStr(invites[roomA]), jsonArrStr(inviteStateA))
	}
	if !reflect.DeepEqual(invites[roomB], inviteStateB) {
		t.Errorf("room %s got %s want %s", roomA, jsonArrStr(invites[roomA]), jsonArrStr(inviteStateA))
	}

	// Assert Bob's invites
	invites, err = table.SelectAllInvitesForUser(bob)
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if len(invites) != 1 {
		t.Fatalf("got %d invites, want 1", len(invites))
	}
	if !reflect.DeepEqual(invites[roomA], inviteStateB) {
		t.Errorf("room %s got %s want %s", roomA, jsonArrStr(invites[roomA]), jsonArrStr(inviteStateB))
	}
	bobInvite, err := table.SelectInviteState(bob, roomA)
	if err != nil {
		t.Fatalf("failed to SelectInviteState: %s", err)
	}
	if !reflect.DeepEqual(bobInvite, inviteStateB) {
		t.Errorf("SelectInviteState: got %v want %v", bobInvite, inviteStateB)
	}

	// Assert no-ones invites
	invites, err = table.SelectAllInvitesForUser("no one")
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if len(invites) != 0 {
		t.Fatalf("got %d invites, want 0", len(invites))
	}

	// Update alice's invite, clobber and re-query (inviteState A -> B)
	if err := table.InsertInvite(alice, roomA, inviteStateB); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}
	invites, err = table.SelectAllInvitesForUser(alice)
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if !reflect.DeepEqual(invites[roomA], inviteStateB) {
		t.Errorf("room %s got %s want %s", roomA, jsonArrStr(invites[roomA]), jsonArrStr(inviteStateB))
	}

	// Retire one of Alice's invites and re-query
	if err = table.RemoveInvite(alice, roomA); err != nil {
		t.Fatalf("failed to RemoveInvite: %s", err)
	}
	invites, err = table.SelectAllInvitesForUser(alice)
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if len(invites) != 1 {
		t.Fatalf("got %v invites, want 1", len(invites))
	}
	if !reflect.DeepEqual(invites[roomB], inviteStateB) {
		t.Errorf("room %s got %s want %s", roomB, jsonArrStr(invites[roomB]), jsonArrStr(inviteStateB))
	}

	// Retire Bob's non-existent invite
	if err = table.RemoveInvite(bob, "!nothing"); err != nil {
		t.Fatalf("failed to RemoveInvite: %s", err)
	}
	invites, err = table.SelectAllInvitesForUser(bob)
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if len(invites) != 1 {
		t.Fatalf("got %v invites, want 1", len(invites))
	}

	// Retire Bob's invite
	if err = table.RemoveInvite(bob, roomA); err != nil {
		t.Fatalf("failed to RemoveInvite: %s", err)
	}
	invites, err = table.SelectAllInvitesForUser(bob)
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if len(invites) != 0 {
		t.Fatalf("got %v invites, want 0", len(invites))
	}

	// Retire no-ones invite, no-ops
	if err = table.RemoveInvite("no one", roomA); err != nil {
		t.Fatalf("failed to RemoveInvite: %s", err)
	}
}

func TestInviteTable_RemoveSupersededInvites(t *testing.T) {
	db, close := connectToDB(t)
	defer close()

	alice := "@alice:localhost"
	bob := "@bob:localhost"
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	inviteState := []json.RawMessage{[]byte(`{"foo":"bar"}`)}

	table := NewInvitesTable(db)
	t.Log("Invite Alice and Bob to both rooms.")

	// Add some invites
	if err := table.InsertInvite(alice, roomA, inviteState); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}
	if err := table.InsertInvite(bob, roomA, inviteState); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}
	if err := table.InsertInvite(alice, roomB, inviteState); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}
	if err := table.InsertInvite(bob, roomB, inviteState); err != nil {
		t.Fatalf("failed to InsertInvite: %s", err)
	}

	t.Log("Alice joins room A. Remove her superseded invite.")
	newEvents := []Event{
		{
			Type:       "m.room.member",
			StateKey:   alice,
			Membership: "join",
			RoomID:     roomA,
		},
	}
	err := sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		return table.RemoveSupersededInvites(txn, roomA, newEvents)
	})
	if err != nil {
		t.Fatalf("failed to RemoveSupersededInvites: %s", err)
	}

	t.Log("Alice should still be invited to room B.")
	assertInvites(t, table, alice, map[string][]json.RawMessage{roomB: inviteState})
	t.Log("Bob should still be invited to rooms A and B.")
	assertInvites(t, table, bob, map[string][]json.RawMessage{roomA: inviteState, roomB: inviteState})

	t.Log("Bob declines his invitation to room B.")
	newEvents = []Event{
		{
			Type:       "m.room.member",
			StateKey:   bob,
			Membership: "leave",
			RoomID:     roomB,
		},
	}
	err = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		return table.RemoveSupersededInvites(txn, roomB, newEvents)
	})
	if err != nil {
		t.Fatalf("failed to RemoveSupersededInvites: %s", err)
	}

	t.Log("Alice should still be invited to room B.")
	assertInvites(t, table, alice, map[string][]json.RawMessage{roomB: inviteState})
	t.Log("Bob should still be invited to room A.")
	assertInvites(t, table, bob, map[string][]json.RawMessage{roomA: inviteState})

	// Now try multiple membership changes in one call.
	t.Log("Alice joins, changes profile, leaves and is re-invited to room B.")
	newEvents = []Event{
		{
			Type:       "m.room.member",
			StateKey:   alice,
			Membership: "join",
			RoomID:     roomB,
		},
		{
			Type:       "m.room.member",
			StateKey:   alice,
			Membership: "_join",
			RoomID:     roomB,
		},
		{
			Type:       "m.room.member",
			StateKey:   alice,
			Membership: "leave",
			RoomID:     roomB,
		},
		{
			Type:       "m.room.member",
			StateKey:   alice,
			Membership: "invite",
			RoomID:     roomB,
		},
	}

	err = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		return table.RemoveSupersededInvites(txn, roomB, newEvents)
	})
	if err != nil {
		t.Fatalf("failed to RemoveSupersededInvites: %s", err)
	}

	t.Log("Alice should still be invited to room B.")
	assertInvites(t, table, alice, map[string][]json.RawMessage{roomB: inviteState})

	t.Log("Bob declines, is reinvited to and joins room A.")
	newEvents = []Event{
		{
			Type:       "m.room.member",
			StateKey:   bob,
			Membership: "leave",
			RoomID:     roomA,
		},
		{
			Type:       "m.room.member",
			StateKey:   bob,
			Membership: "invite",
			RoomID:     roomA,
		},
		{
			Type:       "m.room.member",
			StateKey:   bob,
			Membership: "join",
			RoomID:     roomA,
		},
	}

	err = sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		return table.RemoveSupersededInvites(txn, roomA, newEvents)
	})
	if err != nil {
		t.Fatalf("failed to RemoveSupersededInvites: %s", err)
	}
	assertInvites(t, table, bob, map[string][]json.RawMessage{})
}

func assertInvites(t *testing.T, table *InvitesTable, user string, expected map[string][]json.RawMessage) {
	invites, err := table.SelectAllInvitesForUser(user)
	if err != nil {
		t.Fatalf("failed to SelectAllInvitesForUser: %s", err)
	}
	if !reflect.DeepEqual(invites, expected) {
		t.Fatalf("got %v invites, want %v", invites, expected)
	}
}

func jsonArrStr(a []json.RawMessage) (result string) {
	for _, e := range a {
		result += string(e) + "\n"
	}
	return
}
