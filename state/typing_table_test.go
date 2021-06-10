package state

import (
	"reflect"
	"testing"

	"github.com/jmoiron/sqlx"
)

func TestTypingTable(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	userIDs := []string{
		"@alice:localhost",
		"@bob:localhost",
	}
	roomID := "!foo:localhost"
	table := NewTypingTable(db)

	setAndCheck := func() {
		err = table.SetTyping(roomID, userIDs)
		if err != nil {
			t.Fatalf("failed to SetTyping: %s", err)
		}
		gotUserIDs, err := table.Typing(roomID)
		if err != nil {
			t.Fatalf("failed to Typing: %s", err)
		}
		if !reflect.DeepEqual(gotUserIDs, userIDs) {
			t.Errorf("got typing users %v want %v", gotUserIDs, userIDs)
		}
	}
	setAndCheck()
	userIDs = userIDs[1:]
	userIDs = append(userIDs, "@charlie:localhost")
	setAndCheck()
	userIDs = []string{}
	setAndCheck()
}
