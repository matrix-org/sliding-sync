package state

import (
	"encoding/json"

	"github.com/jmoiron/sqlx"
)

// InvitesTable stores invites for each user.
// Originally, invites were stored with the main events in a room. We ignored stripped state and
// just kept the m.room.member invite event. This had many problems though:
//   - The room would be initialised by the invite event, causing the room to not populate with state
//     correctly when the user joined the room.
//   - The user could read room data in the room without being joined to the room e.g could pull
//     `required_state` and `timeline` as they would be authorised by the invite to see this data.
// Instead, we now completely split out invites from the normal event flow. This fixes the issues
// outlined above but introduce more problems:
//   - How do you sort the invite with rooms?
//   - How do you calculate the room name when you lack heroes?
// For now, we say that invites:
//   - are treated as a highlightable event for the purposes of sorting by highlight count.
//   - are given the timestamp of when the invite arrived.
//   - calculate the room name on a best-effort basis given the lack of heroes (same as element-web).
// When an invite is rejected, it appears in the `leave` section which then causes the invite to be
// removed from this table.
type InvitesTable struct {
	db *sqlx.DB
}

func NewInvitesTable(db *sqlx.DB) *InvitesTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_invites (
		room_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		-- JSON array. The contents of 'rooms.invite.$room_id.invite_state.events'
		invite_state BYTEA NOT NULL,
		UNIQUE(user_id, room_id)
	);
	`)
	return &InvitesTable{db}
}

func (t *InvitesTable) RemoveInvite(userID, roomID string) error {
	_, err := t.db.Exec(`DELETE FROM syncv3_invites WHERE user_id = $1 AND room_id = $2`, userID, roomID)
	return err
}

func (t *InvitesTable) InsertInvite(userID, roomID string, inviteRoomState []json.RawMessage) error {
	blob, err := json.Marshal(inviteRoomState)
	if err != nil {
		return err
	}
	_, err = t.db.Exec(
		`INSERT INTO syncv3_invites(user_id, room_id, invite_state) VALUES($1,$2,$3)
		ON CONFLICT (user_id, room_id) DO UPDATE SET invite_state = $3`,
		userID, roomID, blob,
	)
	return err
}

// Select all invites for this user. Returns a map of room ID to invite_state (json array).
func (t *InvitesTable) SelectAllInvitesForUser(userID string) (map[string][]json.RawMessage, error) {
	rows, err := t.db.Query(`SELECT room_id, invite_state FROM syncv3_invites WHERE user_id = $1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]json.RawMessage)
	var roomID string
	var blob json.RawMessage
	for rows.Next() {
		if err := rows.Scan(&roomID, &blob); err != nil {
			return nil, err
		}
		var inviteState []json.RawMessage
		if err := json.Unmarshal(blob, &inviteState); err != nil {
			return nil, err
		}
		result[roomID] = inviteState
	}
	return result, nil
}
