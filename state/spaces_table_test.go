package state

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/jmoiron/sqlx"
)

func matchAnyOrder(t *testing.T, gots, wants []SpaceRelation) {
	t.Helper()
	key := func(sr SpaceRelation) string {
		return fmt.Sprintf("%+v", sr)
	}
	sort.Slice(gots, func(i, j int) bool {
		return key(gots[i]) < key(gots[j])
	})
	sort.Slice(wants, func(i, j int) bool {
		return key(wants[i]) < key(wants[j])
	})
	if !reflect.DeepEqual(gots, wants) {
		t.Fatalf("mismatch, got %+v want %+v", gots, wants)
	}
}

func TestSpaces(t *testing.T) {
	db, err := sqlx.Open("postgres", postgresConnectionString)
	if err != nil {
		t.Fatalf("failed to open SQL db: %s", err)
	}
	txn, err := db.Beginx()
	if err != nil {
		t.Fatalf("failed to start txn: %s", err)
	}
	table := NewSpacesTable(db)

	parent := "!parent"
	child1 := "!child1"
	child2 := "!child2"

	// basic insert
	s1 := SpaceRelation{
		Parent:   parent,
		Child:    child1,
		Relation: RelationMSpaceChild,
	}
	s2 := SpaceRelation{
		Parent:      parent,
		Child:       child2,
		Relation:    RelationMSpaceChild,
		Ordering:    "abc",
		IsSuggested: true,
	}
	if err := table.BulkInsert(txn, []SpaceRelation{s1, s2}); err != nil {
		t.Fatalf("BulkInsert: %s", err)
	}

	// basic select
	result, err := table.SelectChildren(txn, []string{parent})
	if err != nil {
		t.Fatalf("SelectChildren: %s", err)
	}
	children, ok := result[parent]
	if !ok || len(children) != 2 {
		t.Fatalf("SelectChildren: want 2 children, got %+v", result)
	}
	matchAnyOrder(t, children, []SpaceRelation{s1, s2})

	// basic update
	s1.Ordering = "xyz"
	s1.IsSuggested = true
	if err := table.BulkInsert(txn, []SpaceRelation{s1}); err != nil {
		t.Fatalf("BulkInsert: %s", err)
	}
	result, err = table.SelectChildren(txn, []string{parent})
	if err != nil {
		t.Fatalf("SelectChildren: %s", err)
	}
	children, ok = result[parent]
	if !ok || len(children) != 2 {
		t.Fatalf("SelectChildren: want 2 children, got %+v", result)
	}
	matchAnyOrder(t, children, []SpaceRelation{s1, s2})

	// basic delete
	if err = table.BulkDelete(txn, []SpaceRelation{s1, s2}); err != nil {
		t.Fatalf("BulkDelete: %s", err)
	}
	result, err = table.SelectChildren(txn, []string{parent})
	if err != nil {
		t.Fatalf("SelectChildren: %s", err)
	}
	if len(result) > 0 {
		t.Fatalf("SelectChildren: expected no results, got %+v", result)
	}
}
