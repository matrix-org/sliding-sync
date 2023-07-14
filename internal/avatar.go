package internal

import (
	"bytes"
	"encoding/json"
)

// sentinel value
const deletedAvatar = "<no-avatar>"

// An AvatarChange represents a change to a room's avatar. There are three cases:
//   - an empty string represents no change, and should be omitted when JSON-serisalised;
//   - the sentinel `avatarNotPresent` represents a room that has never had an avatar,
//     or a room whose avatar has been removed. It is JSON-serialised as null.
//   - All other strings represent the current avatar of the room and JSON-serialise as
//     normal.
type AvatarChange string

func (a AvatarChange) MarshalJSON() ([]byte, error) {
	if a == deletedAvatar {
		return []byte(`null`), nil
	} else {
		return json.Marshal(string(a))
	}
}

// Note: the unmarshalling is only used in tests.
func (a *AvatarChange) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*a = deletedAvatar
		return nil
	}
	return json.Unmarshal(data, (*string)(a))
}

var DeletedAvatar = AvatarChange(deletedAvatar)
var UnchangedAvatar AvatarChange
