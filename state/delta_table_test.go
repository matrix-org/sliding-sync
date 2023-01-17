package state

import (
	"bytes"
	"testing"

	"github.com/jmoiron/sqlx"
)

func assertDeltaRow(t *testing.T, got, want *DeltaRow) {
	t.Helper()
	if got.DeviceID != want.DeviceID {
		t.Errorf("device id: got %s want %s", got.DeviceID, want.DeviceID)
	}
	if want.ID > 0 && got.ID != want.ID {
		t.Errorf("id: got %v want %v", got.ID, want.ID)
	}
	if !bytes.Equal(got.Data, want.Data) {
		t.Errorf("data: got %v want %v", string(got.Data), string(want.Data))
	}
}

func TestDeltaTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	table := NewDeltaTable(db)
	deviceID := "dev_id"
	row, err := table.CreateDeltaState(deviceID)
	assertNoError(t, err)
	assertDeltaRow(t, row, &DeltaRow{
		DeviceID: deviceID,
		Data:     []byte{},
	})

	// now add some data
	data := []byte("hello world")
	next := &DeltaRow{
		DeviceID: deviceID,
		ID:       row.ID,
		Data:     data,
	}
	assertNoError(t, table.Update(next))

	// check it was saved
	got, err := table.Load(row.ID, deviceID)
	assertNoError(t, err)
	assertDeltaRow(t, got, next)

	// check we cannot get this state with the same id but different device ID, and same device ID but different ID
	got, err = table.Load(row.ID, "other")
	assertNoError(t, err)
	if got != nil {
		t.Fatalf("got data want nothing: %+v", got)
	}
	got, err = table.Load(234324, deviceID)
	assertNoError(t, err)
	if got != nil {
		t.Fatalf("got data want nothing: %+v", got)
	}
}
