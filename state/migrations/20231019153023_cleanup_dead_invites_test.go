package migrations

import (
	"embed"
	_ "embed"
	"encoding/json"
	_ "github.com/lib/pq"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/pressly/goose/v3"
	"reflect"
	"testing"
)

//go:embed 20231019153023_cleanup_dead_invites.sql
var deadInviteMigration embed.FS

func TestDeadInviteCleanup(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	store := state.NewStorageWithDB(db, false)

	// Alice and Bob have invites to rooms X and Y.
	// Chris has an invite to room Z.
	t.Log("Alice is joined to room X; Bob to room Y. Chris isn't joined to any rooms.")
	_, err := store.DB.Exec(`
		INSERT INTO syncv3_invites(user_id, room_id, invite_state)
		VALUES ('@alice:test', '!x', '[]'),
		       ('@alice:test', '!y', '[]'),
			   ('@bob:test'  , '!x', '[]'),
			   ('@bob:test'  , '!y', '[]'),
			   ('@chris:test', '!z', '[]');
	`)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Alice is joined to room X; Bob banned from room Y. Chris isn't joined to any rooms.")
	_, err = store.DB.Exec(`
		INSERT INTO syncv3_events(
			event_nid,
			event_id,
			before_state_snapshot_id,
			event_replaces_nid,
			room_id,
			event_type,
			state_key,
			prev_batch,
			membership,
			is_state, 
			event, 
			missing_previous
		)
		VALUES (1, '$alice-join-x'  , 0, 0, '!x', 'm.room.member', '@alice:test', '', 'join'  , false, '', false),
		       (2, '$alice-invite-y', 0, 0, '!y', 'm.room.member', '@alice:test', '', 'invite', false, '', false),
		       (3, '$bob-invite-x'  , 0, 0, '!x', 'm.room.member', '@bob:test'  , '', 'invite', false, '', false),
		       (4, '$bob-ban-y'     , 0, 0, '!y', 'm.room.member', '@bob:test'  , '', 'ban'   , false, '', false),
		       (5, '$chris-invite-z', 0, 0, '!x', 'm.room.member', '@chris:test', '', 'invite', false, '', false);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB.Exec(`
		INSERT INTO syncv3_snapshots(snapshot_id, room_id, events, membership_events)
		VALUES (10, '!x', '{}', '{1,3}'),
		       (11, '!y', '{}', '{2,4}'),
		       (12, '!z', '{}', '{5}');
	`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.DB.Exec(`
		INSERT INTO syncv3_rooms(room_id, current_snapshot_id, is_encrypted, upgraded_room_id, predecessor_room_id, latest_nid, type)
		VALUES ('!x', '10', false, NULL, NULL, 3, NULL),
		       ('!y', '11', false, NULL, NULL, 4, NULL),
		       ('!z', '12', false, NULL, NULL, 5, NULL);
	`)
	if err != nil {
		t.Fatal(err)
	}

	t.Log("Run the migration.")
	goose.SetBaseFS(deadInviteMigration)
	err = goose.SetDialect("postgres")
	if err != nil {
		t.Fatal(err)
	}

	err = goose.Up(db.DB, ".")
	if err != nil {
		t.Fatal(err)
	}

	emptyInviteState := []json.RawMessage{}

	t.Log("Alice's invite to room X, and Bob's invite to room Y should have been deleted.")
	aliceInvites, err := store.InvitesTable.SelectAllInvitesForUser("@alice:test")
	if err != nil {
		t.Error(err)
	}
	assertInvites(t, "alice,", aliceInvites, map[string][]json.RawMessage{
		"!y": emptyInviteState,
	})

	bobInvites, err := store.InvitesTable.SelectAllInvitesForUser("@bob:test")
	if err != nil {
		t.Error(err)
	}
	assertInvites(t, "bob,", bobInvites, map[string][]json.RawMessage{
		"!x": emptyInviteState,
	})

	chrisInvites, err := store.InvitesTable.SelectAllInvitesForUser("@chris:test")
	if err != nil {
		t.Error(err)
	}
	assertInvites(t, "chris,", chrisInvites, map[string][]json.RawMessage{
		"!z": emptyInviteState,
	})

}

func assertInvites(t *testing.T, name string, got, want map[string][]json.RawMessage) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Mismatched invites for %s: got %v want %v", name, got, want)
	}
}
