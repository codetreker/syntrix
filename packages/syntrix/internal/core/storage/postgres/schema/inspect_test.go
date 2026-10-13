package schema

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInspectDefinitions(t *testing.T) {
	spec := Specification{Table: "owned", Columns: []Column{{Name: "id", Type: "bigint"}}, Indexes: []Index{{Name: "owned_pkey", Keys: []string{"id"}, Unique: true}}, Constraints: []Constraint{{Name: "owned_pkey", Definition: "PRIMARY KEY (id)"}}}
	for _, test := range []struct{ name, state string }{
		{"present", "present"}, {"missing table", "missing"}, {"missing index", "missing"}, {"missing constraint", "missing"},
		{"wrong column type", "conflict"}, {"wrong uniqueness", "conflict"}, {"invalid index", "conflict"}, {"wrong ordering", "conflict"}, {"wrong key", "conflict"}, {"wrong constraint", "conflict"}, {"unvalidated constraint", "conflict"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			row := sqlmock.NewRows([]string{"oid"})
			if test.name == "missing table" {
				row.AddRow(nil)
			} else {
				row.AddRow(1)
			}
			mock.ExpectQuery("SELECT to_regclass").WithArgs("owned").WillReturnRows(row)
			if test.name != "missing table" {
				typ := "bigint"
				if test.name == "wrong column type" {
					typ = "integer"
				}
				mock.ExpectQuery("SELECT a.attname").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "nullable", "default"}).AddRow("id", typ, false, ""))
				if test.name != "wrong column type" {
					index := mock.ExpectQuery("SELECT i.indexrelid").WithArgs(int64(1), "owned_pkey")
					if test.name == "missing index" {
						index.WillReturnRows(sqlmock.NewRows([]string{"oid", "unique", "valid", "ready", "method", "count", "predicate", "directions"}))
					} else {
						unique := test.name != "wrong uniqueness"
						valid := test.name != "invalid index"
						direction := "0"
						if test.name == "wrong ordering" {
							direction = "3"
						}
						index.WillReturnRows(sqlmock.NewRows([]string{"oid", "unique", "valid", "ready", "method", "count", "predicate", "directions"}).AddRow(2, unique, valid, true, "btree", 1, "", direction))
						if unique && valid && direction == "0" {
							key := "id"
							if test.name == "wrong key" {
								key = "other"
							}
							mock.ExpectQuery("SELECT pg_get_indexdef").WithArgs(int64(2), 1).WillReturnRows(sqlmock.NewRows([]string{"key"}).AddRow(key))
						}
					}
					if test.state != "conflict" || test.name == "wrong constraint" || test.name == "unvalidated constraint" {
						constraint := mock.ExpectQuery("SELECT pg_get_constraintdef").WithArgs(int64(1), "owned_pkey")
						if test.name == "missing constraint" {
							constraint.WillReturnRows(sqlmock.NewRows([]string{"definition", "validated"}))
						} else {
							definition := "PRIMARY KEY (id)"
							if test.name == "wrong constraint" {
								definition = "UNIQUE (id)"
							}
							constraint.WillReturnRows(sqlmock.NewRows([]string{"definition", "validated"}).AddRow(definition, test.name != "unvalidated constraint"))
						}
					}
				}
			}
			state, err := Inspect(context.Background(), db, spec)
			require.NoError(t, err)
			assert.Equal(t, test.state, state)
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestInspectErrors(t *testing.T) {
	failure := errors.New("catalog unavailable")
	for _, stage := range []string{"oid", "columns", "column decode", "column rows", "index", "key", "constraint"} {
		t.Run(stage, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			spec := Specification{Table: "owned", Columns: []Column{{Name: "id", Type: "bigint"}}, Indexes: []Index{{Name: "key", Keys: []string{"id"}}}, Constraints: []Constraint{{Name: "constraint", Definition: "CHECK (id>0)"}}}
			if stage == "oid" {
				mock.ExpectQuery("SELECT to_regclass").WillReturnError(failure)
			} else {
				mock.ExpectQuery("SELECT to_regclass").WillReturnRows(sqlmock.NewRows([]string{"oid"}).AddRow(1))
				columns := mock.ExpectQuery("SELECT a.attname")
				if stage == "columns" {
					columns.WillReturnError(failure)
				} else {
					rows := sqlmock.NewRows([]string{"name", "type", "nullable", "default"}).AddRow("id", "bigint", false, "")
					if stage == "column decode" {
						rows = sqlmock.NewRows([]string{"name", "type", "nullable", "default"}).AddRow("id", "bigint", "invalid boolean", "")
					}
					if stage == "column rows" {
						rows.RowError(0, failure)
					}
					columns.WillReturnRows(rows)
					if stage != "column decode" && stage != "column rows" {
						index := mock.ExpectQuery("SELECT i.indexrelid")
						if stage == "index" {
							index.WillReturnError(failure)
						} else {
							index.WillReturnRows(sqlmock.NewRows([]string{"oid", "unique", "valid", "ready", "method", "count", "predicate", "directions"}).AddRow(2, false, true, true, "btree", 1, "", "0"))
							key := mock.ExpectQuery("SELECT pg_get_indexdef")
							if stage == "key" {
								key.WillReturnError(failure)
							} else {
								key.WillReturnRows(sqlmock.NewRows([]string{"key"}).AddRow("id"))
								mock.ExpectQuery("SELECT pg_get_constraintdef").WillReturnError(failure)
							}
						}
					}
				}
			}
			_, err = Inspect(context.Background(), db, spec)
			require.Error(t, err)
			if stage != "column decode" {
				assert.ErrorIs(t, err, failure)
			}
			assert.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
