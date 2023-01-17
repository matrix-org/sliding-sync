package delta

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/state"
)

func newAccountData(id int64) state.AccountData {
	return state.AccountData{
		ID:     id,
		UserID: fmt.Sprintf("user_%d", id),
		RoomID: "",
		Type:   "dummy",
		Data:   []byte("yep"),
	}
}

func TestDeltaStateAccountData(t *testing.T) {
	testCases := []struct {
		name           string
		in             *State
		inAccountData  []state.AccountData
		outAccountData []state.AccountData
	}{
		{
			name: "no prev state",
			in: &State{
				ID: 1, DeviceID: "f", Data: &StateData{},
			},
			inAccountData: []state.AccountData{
				newAccountData(100), newAccountData(101),
			},
			outAccountData: []state.AccountData{
				newAccountData(100), newAccountData(101),
			},
		},
		{
			name: "partial prev state, id present in list",
			in: &State{
				ID: 1, DeviceID: "f", Data: &StateData{
					Account: DeltaAccount{
						AccountID: 100,
					},
				},
			},
			inAccountData: []state.AccountData{
				newAccountData(100), newAccountData(101),
			},
			outAccountData: []state.AccountData{
				newAccountData(101),
			},
		},
		{
			name: "partial prev state, id not present in list",
			in: &State{
				ID: 1, DeviceID: "f", Data: &StateData{
					Account: DeltaAccount{
						AccountID: 100,
					},
				},
			},
			inAccountData: []state.AccountData{
				newAccountData(98), newAccountData(101),
			},
			outAccountData: []state.AccountData{
				newAccountData(101),
			},
		},
		{
			name: "all prev state",
			in: &State{
				ID: 1, DeviceID: "f", Data: &StateData{
					Account: DeltaAccount{
						AccountID: 100,
					},
				},
			},
			inAccountData: []state.AccountData{
				newAccountData(9), newAccountData(10),
			},
			outAccountData: []state.AccountData{},
		},
	}
	for _, tc := range testCases {
		gotAccDatas := tc.in.AddAccountData(tc.inAccountData)
		if !reflect.DeepEqual(gotAccDatas, tc.outAccountData) {
			t.Errorf("%s: got acc data %+v want %+v", tc.name, gotAccDatas, tc.outAccountData)
		}
	}
}
