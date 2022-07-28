package state

import (
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
	"github.com/matrix-org/sync-v3/sqlutil"
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

type SpaceRelationChunker []SpaceRelation

func (c SpaceRelationChunker) Len() int {
	return len(c)
}
func (c SpaceRelationChunker) Subslice(i, j int) sqlutil.Chunker {
	return c[i:j]
}
