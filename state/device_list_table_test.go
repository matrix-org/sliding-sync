package state

import (
	"testing"

	"github.com/matrix-org/sliding-sync/internal"
)

// Tests the DeviceLists table
func TestDeviceListTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	table := NewDeviceListTable(db)
	userID := "@TestDeviceListTable"
	deviceID := "BOB"

	// these are individual updates from Synapse from /sync v2
	deltas := []internal.MapStringInt{
		{
			"alice": internal.DeviceListChanged,
		},
		{
			"💣": internal.DeviceListChanged,
		},
	}
	// apply them
	for _, dd := range deltas {
		err := table.Upsert(userID, deviceID, dd)
		assertNoError(t, err)
	}

	// check we can read-only select. This doesn't modify any fields.
	for i := 0; i < 3; i++ {
		got, err := table.Select(userID, deviceID, false)
		assertNoError(t, err)
		// until we "swap" we don't consume the New entries
		assertVal(t, "unexpected data on swapless select", got, internal.MapStringInt{})
	}
	// now swap-er-roo, which shifts everything from New into Sent.
	got, err := table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertVal(t, "did not select what was upserted on swap select", got, internal.MapStringInt{
		"alice": internal.DeviceListChanged,
		"💣":     internal.DeviceListChanged,
	})

	// this is permanent, read-only views show this too.
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertVal(t, "swapless select did not return the same data as before", got, internal.MapStringInt{
		"alice": internal.DeviceListChanged,
		"💣":     internal.DeviceListChanged,
	})

	// We now expect empty DeviceLists, as we swapped twice.
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertVal(t, "swap select did not return nothing", got, internal.MapStringInt{})

	// get back the original state
	assertNoError(t, err)
	for _, dd := range deltas {
		err = table.Upsert(userID, deviceID, dd)
		assertNoError(t, err)
	}
	// Move original state to Sent by swapping
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertVal(t, "did not select what was upserted on swap select", got, internal.MapStringInt{
		"alice": internal.DeviceListChanged,
		"💣":     internal.DeviceListChanged,
	})
	// Add new entries to New before acknowledging Sent
	err = table.Upsert(userID, deviceID, internal.MapStringInt{
		"💣":       internal.DeviceListChanged,
		"charlie": internal.DeviceListLeft,
	})
	assertNoError(t, err)

	// Reading without swapping does not move New->Sent, so returns the previous value
	got, err = table.Select(userID, deviceID, false)
	assertNoError(t, err)
	assertVal(t, "swapless select did not return the same data as before", got, internal.MapStringInt{
		"alice": internal.DeviceListChanged,
		"💣":     internal.DeviceListChanged,
	})

	// Append even more items to New
	err = table.Upsert(userID, deviceID, internal.MapStringInt{
		"charlie": internal.DeviceListChanged, // we previously said "left" for charlie, so as "changed" is newer, we should see "changed"
		"dave":    internal.DeviceListLeft,
	})
	assertNoError(t, err)

	// Now swap: all the combined items in New go into Sent
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertVal(t, "swap select did not return combined new items", got, internal.MapStringInt{
		"💣":       internal.DeviceListChanged,
		"charlie": internal.DeviceListChanged,
		"dave":    internal.DeviceListLeft,
	})

	// Swapping again clears Sent out, and since nothing is in New we get an empty list
	got, err = table.Select(userID, deviceID, true)
	assertNoError(t, err)
	assertVal(t, "swap select did not return combined new items", got, internal.MapStringInt{})
}
