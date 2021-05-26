package state

import "testing"

func TestEventTable(t *testing.T) {
	table := NewEventTable("user=kegan dbname=syncv3 sslmode=disable")
	events := []Event{
		{
			ID:   "100",
			JSON: []byte(`{"event_id":"100", "foo":"bar"}`),
		},
		{
			ID:   "101",
			JSON: []byte(`{"event_id":"101",  "foo":"bar"}`),
		},
		{
			// ID is optional, it will pull event_id out if it's missing
			JSON: []byte(`{"event_id":"102", "foo":"bar"}`),
		},
	}
	if err := table.Insert(events); err != nil {
		t.Fatalf("Insert failed: %s", err)
	}
	// duplicate insert is ok
	if err := table.Insert(events); err != nil {
		t.Fatalf("Insert failed: %s", err)
	}

	// pulling non-existent ids returns no error but a zero slice
	events, err := table.SelectByIDs([]string{"101010101010"})
	if err != nil {
		t.Fatalf("SelectByIDs failed: %s", err)
	}
	if len(events) != 0 {
		t.Fatalf("SelectByIDs returned %d events, wanted none", len(events))
	}

	// pulling events by event_id is ok
	events, err = table.SelectByIDs([]string{"100", "101", "102"})
	if err != nil {
		t.Fatalf("SelectByIDs failed: %s", err)
	}
	if len(events) != 3 {
		t.Fatalf("SelectByIDs returned %d events, want 3", len(events))
	}

	var nids []int
	for _, ev := range events {
		nids = append(nids, ev.NID)
	}
	// pulling events by event nid is ok
	events, err = table.SelectByNIDs(nids)
	if err != nil {
		t.Fatalf("SelectByNIDs failed: %s", err)
	}
	if len(events) != 3 {
		t.Fatalf("SelectByNIDs returned %d events, want 3", len(events))
	}

	// pulling non-existent nids returns no error but a zero slice
	events, err = table.SelectByNIDs([]int{9999999})
	if err != nil {
		t.Fatalf("SelectByNIDs failed: %s", err)
	}
	if len(events) != 0 {
		t.Fatalf("SelectByNIDs returned %d events, wanted none", len(events))
	}

}
