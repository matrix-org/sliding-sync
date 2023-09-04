package state

/*
TODO: revisit test
func TestMembershipsTable(t *testing.T) {
	db, close := connectToDB(t)
	defer close()
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	defer txn.Commit()

	membershipsTable := NewMembershipsTable(db)
	_ = membershipsTable

	aliceUserID := "@alice:localhost"
	bobUserID := "@bob:localhost"
	_ = bobUserID

	var eventNID int64 = 1

	snapshotRow := &SnapshotRow{
		SnapshotID:       1,
		MembershipEvents: pq.Int64Array([]int64{eventNID}),
	}

	err = membershipsTable.Insert(txn, aliceUserID, eventNID, snapshotRow)
	if err != nil {
		t.Fatal(err)
	}

	// validate the membership was inserted
	snapshots := []int64{snapshotRow.SnapshotID}
	eventNIDs, err := membershipsTable.selectEventNIDs(txn, []string{aliceUserID}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventNIDs) == 0 {
		t.Fatal("expected 1 eventNID, got 0")
	}
	if eventNIDs[0] != eventNID {
		t.Fatalf("expected eventNID %d, got %d", eventNID, eventNIDs[0])
	}

	// create a new snapshot
	snapshotRow.SnapshotID++
	err = membershipsTable.Insert(txn, aliceUserID, eventNID, snapshotRow)
	if err != nil {
		t.Fatal(err)
	}
	// check that we can still get the eventNID for the appended snapshotID
	snapshots = []int64{snapshotRow.SnapshotID}
	t.Logf("%#v", snapshots)
	eventNIDs, err = membershipsTable.selectEventNIDs(txn, []string{aliceUserID}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventNIDs) == 0 {
		t.Fatal("expected 1 eventNID, got 0")
	}
	if eventNIDs[0] != eventNID {
		t.Fatalf("expected eventNID %d, got %d", eventNID, eventNIDs[0])
	}

	// Now insert bob to the same snapshot
	eventNID++
	err = membershipsTable.Insert(txn, bobUserID, eventNID, snapshotRow)
	if err != nil {
		t.Fatal(err)
	}

	// Validate bob is there
	eventNIDs, err = membershipsTable.selectEventNIDs(txn, []string{bobUserID}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventNIDs) == 0 {
		t.Fatal("expected 1 eventNID, got 0")
	}
	if eventNIDs[0] != eventNID {
		t.Fatalf("expected eventNID %d, got %d", eventNID, eventNIDs[0])
	}

	// check that we can query alice AND bob in snapshot 2
	snapshots = append(snapshots, 1)
	eventNIDs, err = membershipsTable.selectEventNIDs(txn, []string{aliceUserID, bobUserID}, snapshots)
	if err != nil {
		t.Fatal(err)
	}
	if len(eventNIDs) == 2 {
		t.Fatal("expected 2 eventNID, got 0")
	}
}
*/
