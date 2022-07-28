package state

import (
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sync-v3/sqlutil"
	"github.com/tidwall/gjson"
)

const (
	RelationMSpaceParent = 1
	RelationMSpaceChild  = 2
)

type SpaceRelation struct {
	Parent      string `db:"parent"`
	Child       string `db:"child"`
	Relation    int    `db:"relation"`
	Ordering    string `db:"ordering"`
	IsSuggested bool   `db:"suggested"`
}

func (sr *SpaceRelation) Key() string {
	return fmt.Sprintf("%s-%s-%d", sr.Parent, sr.Child, sr.Relation)
}

// Returns a space relation from a compatible event, else nil.
func NewSpaceRelationFromEvent(ev Event) (sr *SpaceRelation, isDeleted bool) {
	event := gjson.ParseBytes(ev.JSON)
	if !event.Get("state_key").Exists() {
		return nil, false
	}
	switch ev.Type {
	case "m.space.child":
		return &SpaceRelation{
			Parent:      ev.RoomID,
			Child:       ev.StateKey,
			Relation:    RelationMSpaceChild,
			Ordering:    event.Get("content.ordering").Str,
			IsSuggested: event.Get("content.suggested").Bool(),
		}, !event.Get("content.via").IsArray()
	case "m.space.parent":
		return &SpaceRelation{
			Parent:   ev.StateKey,
			Child:    ev.RoomID,
			Relation: RelationMSpaceParent,
			// parent events have a Canonical field but we don't care?
		}, !event.Get("content.via").IsArray()
	default:
		return nil, false
	}
}

// SpacesTable stores the space graph for all users.
type SpacesTable struct{}

func NewSpacesTable(db *sqlx.DB) *SpacesTable {
	// make sure tables are made
	db.MustExec(`
	CREATE TABLE IF NOT EXISTS syncv3_spaces (
		parent TEXT NOT NULL,
		child TEXT NOT NULL,
		relation SMALLINT NOT NULL,
		suggested BOOL NOT NULL,
		ordering TEXT NOT NULL, -- "" for unset
		UNIQUE(parent, child, relation)
	);
	`)
	return &SpacesTable{}
}

// Insert space relations by (parent, child, relation)
func (t *SpacesTable) BulkInsert(txn *sqlx.Tx, relations []SpaceRelation) error {
	if len(relations) == 0 {
		return nil
	}
	chunks := sqlutil.Chunkify(5, MaxPostgresParameters, SpaceRelationChunker(relations))
	for _, chunk := range chunks {
		_, err := txn.NamedExec(`
		INSERT INTO syncv3_spaces (parent, child, relation, ordering, suggested)
        VALUES (:parent, :child, :relation, :ordering, :suggested) ON CONFLICT (parent, child, relation) DO UPDATE SET ordering = EXCLUDED.ordering, suggested = EXCLUDED.suggested`, chunk)
		if err != nil {
			return err
		}
	}
	return nil
}

// Delete space relations by (parent, child, relation)
func (t *SpacesTable) BulkDelete(txn *sqlx.Tx, relations []SpaceRelation) error {
	if len(relations) == 0 {
		return nil
	}
	for _, r := range relations {
		_, err := txn.Exec(
			`DELETE FROM syncv3_spaces WHERE parent=$1 AND child=$2 AND relation=$3`, r.Parent, r.Child, r.Relation,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// Select all children for these spaces
func (t *SpacesTable) SelectChildren(txn *sqlx.Tx, spaces []string) (map[string][]SpaceRelation, error) {
	result := make(map[string][]SpaceRelation)
	var data []SpaceRelation
	err := txn.Select(&data, `SELECT parent, child, relation, ordering, suggested FROM syncv3_spaces WHERE parent = ANY($1)`, pq.StringArray(spaces))
	if err != nil {
		return nil, err
	}
	// bucket by space
	for _, d := range data {
		result[d.Parent] = append(result[d.Parent], d)
	}
	return result, nil
}

func (t *SpacesTable) HandleSpaceUpdates(txn *sqlx.Tx, events []Event) error {
	// pull out relations, and bucket them so the last event wins to ensure we always use the latest
	// values in case someone repeatedly adds/removes the same space
	relations := make(map[string]struct {
		relation  *SpaceRelation
		isDeleted bool
	})
	for _, ev := range events {
		r, isDeleted := NewSpaceRelationFromEvent(ev)
		if r != nil {
			relations[r.Key()] = struct {
				relation  *SpaceRelation
				isDeleted bool
			}{
				relation:  r,
				isDeleted: isDeleted,
			}
		}
	}

	// now bucket by add/remove for bulk operations
	var added, removed []SpaceRelation
	for _, r := range relations {
		if r.isDeleted {
			removed = append(removed, *r.relation)
		} else {
			added = append(added, *r.relation)
		}
	}
	// update the database
	if err := t.BulkInsert(txn, added); err != nil {
		return fmt.Errorf("failed to BulkInsert: %s", err)
	}
	if err := t.BulkDelete(txn, removed); err != nil {
		return fmt.Errorf("failed to BulkDelete: %s", err)
	}

	return nil
}

type SpaceRelationChunker []SpaceRelation

func (c SpaceRelationChunker) Len() int {
	return len(c)
}
func (c SpaceRelationChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}
