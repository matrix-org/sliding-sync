package state

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestInviteTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
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

func jsonArrStr(a []json.RawMessage) (result string) {
	for _, e := range a {
		result += string(e) + "\n"
	}
	return
}
