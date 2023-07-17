package sync3

import (
	"bytes"
	"encoding/json"
)

// An AvatarChange represents a change to a room's avatar. There are three cases:
//   - an empty string represents no change, and should be omitted when JSON-serialised;
//   - the sentinel `<no-avatar>` represents a room that has never had an avatar,
//     or a room whose avatar has been removed. It is JSON-serialised as null.
//   - All other strings represent the current avatar of the room and JSON-serialise as
//     normal.
type AvatarChange string

const DeletedAvatar = AvatarChange("<no-avatar>")
const UnchangedAvatar AvatarChange = ""

// NewAvatarChange interprets an optional avatar string as an AvatarChange.
func NewAvatarChange(avatar string) AvatarChange {
	if avatar == "" {
		return DeletedAvatar
	}
	return AvatarChange(avatar)
}

func (a AvatarChange) MarshalJSON() ([]byte, error) {
	if a == DeletedAvatar {
		return []byte(`null`), nil
	} else {
		return json.Marshal(string(a))
	}
}

// Note: the unmarshalling is only used in tests.
func (a *AvatarChange) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*a = DeletedAvatar
		return nil
	}
	return json.Unmarshal(data, (*string)(a))
}
