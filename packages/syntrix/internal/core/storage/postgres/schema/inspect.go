package schema

import (
	"context"
	"database/sql"
	"github.com/lib/pq"
	"slices"
	"strings"
)

type Column struct {
	Name, Type, Default string
	Nullable            bool
}
type Index struct {
	Name       string
	Keys       []string
	Descending []bool
	Unique     bool
	Predicate  string
}
type Constraint struct{ Name, Definition string }
type Specification struct {
	Namespace   string       `json:"namespace,omitempty"`
	Table       string       `json:"table"`
	Columns     []Column     `json:"columns"`
	Indexes     []Index      `json:"indexes"`
	Constraints []Constraint `json:"constraints,omitempty"`
}

// Inspect reads only catalog definitions. Missing objects remain preparation
// work; an existing incompatible object is never rewritten by initialization.
func Inspect(ctx context.Context, db *sql.DB, s Specification) (string, error) {
	var oid sql.NullInt64
	qualified := s.Table
	if s.Namespace != "" {
		qualified = pq.QuoteIdentifier(s.Namespace) + "." + pq.QuoteIdentifier(s.Table)
	}
	if err := db.QueryRowContext(ctx, "SELECT to_regclass($1)::oid", qualified).Scan(&oid); err != nil {
		return "", err
	}
	if !oid.Valid {
		return "missing", nil
	}
	rows, err := db.QueryContext(ctx, `SELECT a.attname, format_type(a.atttypid,a.atttypmod), NOT a.attnotnull, COALESCE(pg_get_expr(d.adbin,d.adrelid),'') FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, oid.Int64)
	if err != nil {
		return "", err
	}
	columns := []Column{}
	for rows.Next() {
		var c Column
		if err := rows.Scan(&c.Name, &c.Type, &c.Nullable, &c.Default); err != nil {
			rows.Close()
			return "", err
		}
		columns = append(columns, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if !slices.Equal(columns, s.Columns) {
		return "conflict", nil
	}
	state := "present"
	for _, expected := range s.Indexes {
		var unique, valid, ready bool
		var method, predicate, directions string
		var count int
		var indexOID int64
		err := db.QueryRowContext(ctx, `SELECT i.indexrelid, i.indisunique, i.indisvalid, i.indisready, a.amname, i.indnatts, COALESCE(pg_get_expr(i.indpred,i.indrelid),''),i.indoption::text FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am a ON a.oid=c.relam WHERE i.indrelid=$1 AND c.relname=$2`, oid.Int64, expected.Name).Scan(&indexOID, &unique, &valid, &ready, &method, &count, &predicate, &directions)
		if err == sql.ErrNoRows {
			state = "missing"
			continue
		}
		if err != nil {
			return "", err
		}
		if unique != expected.Unique || !valid || !ready || method != "btree" || count != len(expected.Keys) || normalize(predicate) != normalize(expected.Predicate) {
			return "conflict", nil
		}
		flags := strings.Fields(directions)
		if len(flags) != len(expected.Keys) {
			return "conflict", nil
		}
		for i, key := range expected.Keys {
			want := "0"
			if len(expected.Descending) > i && expected.Descending[i] {
				want = "3"
			}
			if flags[i] != want {
				return "conflict", nil
			}
			var actual string
			if err := db.QueryRowContext(ctx, "SELECT pg_get_indexdef($1,$2,true)", indexOID, i+1).Scan(&actual); err != nil {
				return "", err
			}
			if normalize(actual) != normalize(key) {
				return "conflict", nil
			}
		}
	}
	for _, expected := range s.Constraints {
		var actual string
		var validated bool
		err := db.QueryRowContext(ctx, "SELECT pg_get_constraintdef(oid), convalidated FROM pg_constraint WHERE conrelid=$1 AND conname=$2", oid.Int64, expected.Name).Scan(&actual, &validated)
		if err == sql.ErrNoRows {
			state = "missing"
			continue
		}
		if err != nil {
			return "", err
		}
		if !validated || normalize(actual) != normalize(expected.Definition) {
			return "conflict", nil
		}
	}
	return state, nil
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }
