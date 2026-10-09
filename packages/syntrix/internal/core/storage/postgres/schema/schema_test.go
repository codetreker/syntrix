package schema

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestEnsure(t *testing.T) {
	operationErr := errors.New("operation failed")
	rollbackErr := errors.New("rollback failed")
	for _, test := range []struct {
		name     string
		setup    func(sqlmock.Sqlmock)
		wantErrs []error
	}{
		{
			name: "commit all statements",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(lockKey).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("CREATE TABLE one").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("CREATE INDEX two").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit()
			},
		},
		{
			name: "begin failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin().WillReturnError(operationErr)
			},
			wantErrs: []error{operationErr},
		},
		{
			name: "lock failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(lockKey).WillReturnError(operationErr)
				mock.ExpectRollback()
			},
			wantErrs: []error{operationErr},
		},
		{
			name: "later statement failure rolls back earlier DDL",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(lockKey).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("CREATE TABLE one").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("CREATE INDEX two").WillReturnError(operationErr)
				mock.ExpectRollback()
			},
			wantErrs: []error{operationErr},
		},
		{
			name: "rollback failure preserves both causes",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(lockKey).WillReturnError(operationErr)
				mock.ExpectRollback().WillReturnError(rollbackErr)
			},
			wantErrs: []error{operationErr, rollbackErr},
		},
		{
			name: "commit failure",
			setup: func(mock sqlmock.Sqlmock) {
				mock.ExpectBegin()
				mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(lockKey).WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("CREATE TABLE one").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("CREATE INDEX two").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectCommit().WillReturnError(operationErr)
			},
			wantErrs: []error{operationErr},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			test.setup(mock)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err = Ensure(ctx, db, "CREATE TABLE one", "CREATE INDEX two")
			if len(test.wantErrs) == 0 {
				require.NoError(t, err)
			}
			for _, want := range test.wantErrs {
				require.ErrorIs(t, err, want)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestEnsureCanceledLockWait(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(lockKey).
		WillDelayFor(time.Second).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = Ensure(ctx, db, "CREATE TABLE one")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorIs(t, err, sqlmock.ErrCancelled)
	require.Eventually(t, func() bool {
		return mock.ExpectationsWereMet() == nil
	}, time.Second, time.Millisecond)
}

func TestEnsureCanceledBeforeBegin(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, Ensure(ctx, db, "CREATE TABLE one"), context.Canceled)
	require.NoError(t, mock.ExpectationsWereMet())
}
