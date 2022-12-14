package state

import (
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func assertTxns(t *testing.T, gotEventToTxn map[string]string, wantEventToTxn map[string]string) {
	t.Helper()
	if len(gotEventToTxn) != len(wantEventToTxn) {
		t.Errorf("got %d results, want %d", len(gotEventToTxn), len(wantEventToTxn))
	}
	for wantEventID, wantTxnID := range wantEventToTxn {
		gotTxnID, ok := gotEventToTxn[wantEventID]
		if !ok {
			t.Errorf("txn ID for event %v is missing", wantEventID)
			continue
		}
		if gotTxnID != wantTxnID {
			t.Errorf("event %v got txn ID %v want %v", wantEventID, gotTxnID, wantTxnID)
		}
	}
}

func TestTransactionTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	userID := "@alice:txns"
	eventA := "$A"
	eventB := "$B"
	txnIDA := "txn_A"
	txnIDB := "txn_B"
	table := NewTransactionsTable(db)
	// empty table select
	gotTxns, err := table.Select(userID, []string{eventA})
	assertNoError(t, err)
	assertTxns(t, gotTxns, nil)

	// basic insert and select
	err = table.Insert(userID, map[string]string{
		eventA: txnIDA,
	})
	assertNoError(t, err)
	gotTxns, err = table.Select(userID, []string{eventA})
	assertNoError(t, err)
	assertTxns(t, gotTxns, map[string]string{
		eventA: txnIDA,
	})

	// multiple txns
	err = table.Insert(userID, map[string]string{
		eventB: txnIDB,
	})
	assertNoError(t, err)
	gotTxns, err = table.Select(userID, []string{eventA, eventB})
	assertNoError(t, err)
	assertTxns(t, gotTxns, map[string]string{
		eventA: txnIDA,
		eventB: txnIDB,
	})

	// different user select
	gotTxns, err = table.Select("@another", []string{eventA, eventB})
	assertNoError(t, err)
	assertTxns(t, gotTxns, nil)

	// no-op cleanup
	err = table.Clean(time.Now().Add(-1 * time.Minute))
	assertNoError(t, err)
	gotTxns, err = table.Select(userID, []string{eventA, eventB})
	assertNoError(t, err)
	assertTxns(t, gotTxns, map[string]string{
		eventA: txnIDA,
		eventB: txnIDB,
	})

	// real cleanup
	err = table.Clean(time.Now())
	assertNoError(t, err)
	gotTxns, err = table.Select(userID, []string{eventA, eventB})
	assertNoError(t, err)
	assertTxns(t, gotTxns, nil)

}
