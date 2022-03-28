package sync2

import "testing"

func TestTransactionIDCache(t *testing.T) {
	alice := "@alice:localhost"
	bob := "@bob:localhost"
	eventA := "$a:localhost"
	eventB := "$b:localhost"
	eventC := "$c:localhost"
	txn1 := "1"
	txn2 := "2"
	cache := NewTransactionIDCache()
	cache.Store(alice, eventA, txn1)
	cache.Store(bob, eventB, txn1) // different users can use same txn ID
	cache.Store(alice, eventC, txn2)

	testCases := []struct {
		eventID string
		userID  string
		want    string
	}{
		{
			eventID: eventA,
			userID:  alice,
			want:    txn1,
		},
		{
			eventID: eventB,
			userID:  bob,
			want:    txn1,
		},
		{
			eventID: eventC,
			userID:  alice,
			want:    txn2,
		},
		{
			eventID: "$invalid",
			userID:  alice,
			want:    "",
		},
		{
			eventID: eventA,
			userID:  "@invalid",
			want:    "",
		},
	}
	for _, tc := range testCases {
		txnID := cache.Get(tc.userID, tc.eventID)
		if txnID != tc.want {
			t.Errorf("%+v: got %v want %v", tc, txnID, tc.want)
		}
	}
}
