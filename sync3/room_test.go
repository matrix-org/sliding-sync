package sync3

import (
	"encoding/json"
	"fmt"
	"github.com/matrix-org/sliding-sync/internal"
	"github.com/tidwall/gjson"
	"reflect"
	"testing"
)

func TestAvatarChangeMarshalling(t *testing.T) {
	var url = "mxc://..."
	testCases := []struct {
		Name         string
		AvatarChange internal.AvatarChange
		Check        func(avatar gjson.Result) error
	}{
		{
			Name:         "Avatar exists",
			AvatarChange: internal.AvatarChange(url),
			Check: func(avatar gjson.Result) error {
				if !(avatar.Exists() && avatar.Type == gjson.String && avatar.Str == url) {
					return fmt.Errorf("unexpected marshalled avatar: got %#v want %s", avatar, url)
				}
				return nil
			},
		},
		{
			Name:         "Avatar doesn't exist",
			AvatarChange: internal.DeletedAvatar,
			Check: func(avatar gjson.Result) error {
				if !(avatar.Exists() && avatar.Type == gjson.Null) {
					return fmt.Errorf("unexpected marshalled Avatar: got %#v want null", avatar)
				}
				return nil
			},
		},
		{
			Name:         "Avatar unchanged",
			AvatarChange: internal.UnchangedAvatar,
			Check: func(avatar gjson.Result) error {
				if avatar.Exists() {
					return fmt.Errorf("unexpected marshalled Avatar: got %#v want omitted", avatar)
				}
				return nil
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			room := Room{AvatarChange: tc.AvatarChange}
			marshalled, err := json.Marshal(room)
			t.Logf("Marshalled to %s", string(marshalled))
			if err != nil {
				t.Fatal(err)
			}
			avatar := gjson.GetBytes(marshalled, "avatar")
			if err = tc.Check(avatar); err != nil {
				t.Fatal(err)
			}

			var unmarshalled Room
			err = json.Unmarshal(marshalled, &unmarshalled)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("Unmarshalled to %#v", unmarshalled.AvatarChange)
			if !reflect.DeepEqual(unmarshalled, room) {
				t.Fatalf("Unmarshalled struct is different from original")
			}
		})
	}
}
