package delta

import (
	"reflect"
	"testing"

	"github.com/matrix-org/sliding-sync/state"
)

type deltaTableKey struct {
	deviceID string
	id       int64
}

type mockDeltaTable struct {
	lastID  int64
	entries map[deltaTableKey]*state.DeltaRow
}

func (t *mockDeltaTable) CreateDeltaState(deviceID string) (row *state.DeltaRow, err error) {
	t.lastID++
	row = &state.DeltaRow{
		ID:       t.lastID,
		DeviceID: deviceID,
		Data:     []byte{},
	}
	t.entries[deltaTableKey{
		deviceID: deviceID,
		id:       t.lastID,
	}] = row
	return
}
func (t *mockDeltaTable) Load(id int64, deviceID string) (row *state.DeltaRow, err error) {
	return t.entries[deltaTableKey{
		deviceID: deviceID,
		id:       id,
	}], nil
}
func (t *mockDeltaTable) Update(row *state.DeltaRow) error {
	t.entries[deltaTableKey{
		deviceID: row.DeviceID,
		id:       row.ID,
	}] = row
	return nil
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	t.Fatalf("got error: %v", err)
}

func assertDeltaState(t *testing.T, got, want State) {
	t.Helper()
	if want.DeviceID != "" && got.DeviceID != want.DeviceID {
		t.Errorf("device id: got %v want %v", got.DeviceID, want.DeviceID)
	}
	if want.ID != 0 && got.ID != want.ID {
		t.Errorf("id: got %v want %v", got.ID, want.ID)
	}
	if want.Data != nil {
		if !reflect.DeepEqual(*want.Data, *got.Data) {
			t.Errorf("data: got %+v want %+v", *got.Data, *want.Data)
		}
	}
}

func TestManager(t *testing.T) {
	table := &mockDeltaTable{
		entries: make(map[deltaTableKey]*state.DeltaRow),
	}
	deviceID := "FOO"
	deviceID2 := "BAR"
	mng := NewManager(table)

	// check that we can make new delta states for the same and other devices
	ch := mng.AsyncLoadDeltaState(deviceID, "", true)
	delta1 := mng.WaitFor(ch)
	assertDeltaState(t, *delta1, State{
		ID:       1,
		DeviceID: deviceID,
	})
	ch = mng.AsyncLoadDeltaState(deviceID, "", true)
	delta2 := mng.WaitFor(ch)
	assertDeltaState(t, *delta2, State{
		ID:       2,
		DeviceID: deviceID,
	})
	ch = mng.AsyncLoadDeltaState(deviceID2, "", true)
	deltaOther := mng.WaitFor(ch)
	assertDeltaState(t, *deltaOther, State{
		ID:       3,
		DeviceID: deviceID2,
	})

	// pack in different bits of data and save them
	delta1.Data = &StateData{
		Position: 55,
		Account: DeltaAccount{
			AccountID: 230,
		},
	}
	delta2.Data = &StateData{
		Position: 60,
		Account: DeltaAccount{
			AccountID: 240,
		},
	}
	deltaOther.Data = &StateData{
		Position: 77,
		Account: DeltaAccount{
			AccountID: 280,
		},
	}
	assertNoError(t, mng.Save(delta1))
	assertNoError(t, mng.Save(delta2))
	assertNoError(t, mng.Save(deltaOther))

	// now re-querying this data returns it.
	delta1Fetch := mng.WaitFor(mng.AsyncLoadDeltaState(deviceID, delta1.Token(), false))
	assertDeltaState(t, *delta1Fetch, State{
		ID:       1,
		DeviceID: deviceID,
		Data:     delta1.Data,
	})
}
