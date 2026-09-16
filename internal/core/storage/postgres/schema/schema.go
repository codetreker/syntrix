// Package schema coordinates PostgreSQL schema initialization across instances.
package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// All initialization entry points reserve this database-local advisory key
// ("SYNTRIX" followed by 0x01). It must remain shared across schemas and releases:
// search_path aliases can resolve to the same tables.
const lockKey int64 = 0x53594e5452495801

// Ensure commits statements atomically under the shared initialization lock.
func Ensure(ctx context.Context, db *sql.DB, statements ...string) (err error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin schema initialization: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("rollback schema initialization: %w", rollbackErr))
		}
		if err != nil && ctx.Err() != nil && !errors.Is(err, ctx.Err()) {
			err = errors.Join(err, ctx.Err())
		}
	}()

	// Keep lock acquisition separate from DDL so its READ COMMITTED snapshot
	// includes the preceding initializer's commit after a lock wait.
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", lockKey); err != nil {
		return fmt.Errorf("lock schema initialization: %w", err)
	}
	for _, statement := range statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("execute schema initialization: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit schema initialization: %w", err)
	}
	return nil
}
