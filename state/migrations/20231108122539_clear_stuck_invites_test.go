package migrations

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/matrix-org/sliding-sync/sqlutil"
	"github.com/matrix-org/sliding-sync/state"
	"github.com/matrix-org/sliding-sync/sync2"
)

func TestClearStuckInvites(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	roomsTable := state.NewRoomsTable(db)
	inviteTable := state.NewInvitesTable(db)
	unreadTable := state.NewUnreadTable(db)
	deviceTable := sync2.NewDevicesTable(db)
	tokensTable := sync2.NewTokensTable(db, "secret")

	zero := 0
	device1 := "TEST_1"
	device2 := "TEST_2"
	roomA := "!TestClearStuckInvites_a:localhost"
	roomB := "!TestClearStuckInvites_b:localhost"
	roomC := "!TestClearStuckInvites_c:localhost"
	roomD := "!TestClearStuckInvites_d:localhost"
	roomE := "!TestClearStuckInvites_e:localhost"
	roomF := "!TestClearStuckInvites_f:localhost"
	roomG := "!TestClearStuckInvites_g:localhost"
	alice := "@TestClearStuckInvites_alice:localhost"
	bob := "@TestClearStuckInvites_bob:localhost"
	charlie := "@TestClearStuckInvites_charlie:localhost"
	doris := "@TestClearStuckInvites_doris:localhost"
	users := []string{
		alice, bob, charlie, doris,
	}

	// Test cases:
	// Room | In Invite Table? | In Unread Table? | In Room Table? | Comment
	//   A           Y                 Y                Y             OK, Genuine invite, proxy in room
	//   B           Y                 Y                N             BAD, Stuck invite, proxy never saw join
	//   C           Y                 N                Y             OK, Genuine invite, proxy in room, no unread counts (unusual but valid)
	//   D           Y                 N                N             OK, Genuine invite, proxy not in room
	//   E           N                 Y                Y             OK, Genuine joined room
	//   F           N                 Y                N             BAD, Stuck joined room, proxy never saw join
	//   G           N                 N                Y             OK, Genuine joined room, no unread counts (unusual but valid)
	//   -           N                 N                N             Impossible, room id isn't in any table!
	roomToInfo := map[string]struct {
		invitedUser string
		unreadUser  string
		inRoomTable bool
	}{
		roomA: {
			invitedUser: alice,
			unreadUser:  bob,
			inRoomTable: true,
		},
		roomB: {
			invitedUser: bob,
			unreadUser:  bob,
			inRoomTable: false,
		},
		roomC: {
			invitedUser: charlie,
			inRoomTable: true,
		},
		roomD: {
			invitedUser: doris,
			inRoomTable: false,
		},
		roomE: {
			unreadUser:  alice,
			inRoomTable: true,
		},
		roomF: {
			unreadUser: doris,
		},
		roomG: {
			inRoomTable: true,
		},
	}

	err := sqlutil.WithTransaction(db, func(txn *sqlx.Tx) error {
		for roomID, info := range roomToInfo {
			if info.inRoomTable {
				err := roomsTable.Upsert(txn, state.RoomInfo{
					ID: roomID,
				}, 0, 0)
				if err != nil {
					return fmt.Errorf("Upsert room: %s", err)
				}
			}
			if info.invitedUser != "" {
				err := inviteTable.InsertInvite(info.invitedUser, roomID, []json.RawMessage{json.RawMessage(`{}`)})
				if err != nil {
					return fmt.Errorf("InsertInvite: %s", err)
				}
			}
			if info.unreadUser != "" {
				err := unreadTable.UpdateUnreadCounters(info.unreadUser, roomID, &zero, &zero)
				if err != nil {
					return fmt.Errorf("UpdateUnreadCounters: %s", err)
				}
			}
		}
		for _, userID := range users {
			for _, deviceID := range []string{device1, device2} {
				// each user has 2 devices
				if err := deviceTable.InsertDevice(txn, userID, deviceID); err != nil {
					return fmt.Errorf("InsertDevice: %s", err)
				}
				_, err := tokensTable.Insert(txn, userID+deviceID, userID, deviceID, time.Now())
				if err != nil {
					return fmt.Errorf("TokensTable.Insert: %s", err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("failed to set up test configuration: %s", err)
	}
	// set since tokens (this is done without a txn hence cannot be bundled in as the UPDATE would fail)
	for _, userID := range users {
		for i, deviceID := range []string{device1, device2} {
			// each user has 2 devices
			since := fmt.Sprintf("since_%d", i)
			if err := deviceTable.UpdateDeviceSince(userID, deviceID, since); err != nil {
				t.Fatalf("UpdateDeviceSince: %s", err)
			}
		}
	}

	t.Log("Run the migration.")
	tx, err := db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	if err := upClearStuckInvites(context.Background(), tx.Tx); err != nil {
		t.Fatalf("upClearStuckInvites: %s", err)
	}
	tx.Commit()

	// make a new txn for assertions
	tx, err = db.Beginx()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// users in room B (bob) and F (doris) should be reset.
	tokens, err := tokensTable.TokenForEachDevice(tx)
	if err != nil {
		t.Fatalf("TokenForEachDevice: %s", err)
	}
	wantResults := map[[2]string]struct {
		wantSinceReset bool
	}{
		{bob, device1}: {
			wantSinceReset: true,
		},
		{bob, device2}: {
			wantSinceReset: true,
		},
		{doris, device1}: {
			wantSinceReset: true,
		},
		{doris, device2}: {
			wantSinceReset: true,
		},
		// everyone else should NOT have since reset
		{alice, device1}: {
			wantSinceReset: false,
		},
		{alice, device2}: {
			wantSinceReset: false,
		},
		{charlie, device1}: {
			wantSinceReset: false,
		},
		{charlie, device2}: {
			wantSinceReset: false,
		},
	}
	for _, tok := range tokens {
		key := [2]string{tok.UserID, tok.DeviceID}
		want, ok := wantResults[key]
		if !ok {
			continue // different user in another test?
		}
		if want.wantSinceReset && tok.Since != "" {
			t.Errorf("%s want since reset, got %+v", key, tok)
		}
		if !want.wantSinceReset && tok.Since == "" {
			t.Errorf("%s did not want since reset, got %+v", key, tok)
		}
	}
	// invites for Bob and Doris are gone
	for _, userID := range []string{bob, doris} {
		got, err := inviteTable.SelectAllInvitesForUser(userID)
		if err != nil {
			t.Fatalf("SelectAllInvitesForUser: %s", err)
		}
		if len(got) > 0 {
			t.Fatalf("SelectAllInvitesForUser got invites for %s, wanted none: %+v", userID, got)
		}
	}
	// ensure other invites remain
	wantInvites := map[string][]string{
		alice:   {roomA},
		charlie: {roomC},
	}
	for userID, wantInvitesRooms := range wantInvites {
		got, err := inviteTable.SelectAllInvitesForUser(userID)
		if err != nil {
			t.Fatalf("SelectAllInvitesForUser: %s", err)
		}
		if len(got) != len(wantInvitesRooms) {
			t.Fatalf("SelectAllInvitesForUser got %d invites for %s, wanted %d", len(got), userID, len(wantInvitesRooms))
		}
		for _, wantRoom := range wantInvitesRooms {
			_, exists := got[wantRoom]
			if !exists {
				t.Fatalf("SelectAllInvitesForUser wanted invite for %s in %s, but it's missing", userID, wantRoom)
			}
		}
	}
}
