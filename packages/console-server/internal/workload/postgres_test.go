package workload

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func postgresTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CONSOLE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("CONSOLE_TEST_POSTGRES_DSN is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	seed := make([]byte, 8)
	_, err = rand.Read(seed)
	require.NoError(t, err)
	schema := "console_workload_test_" + hex.EncodeToString(seed)
	_, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err)
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		require.NoError(t, err)
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(12)
	require.NoError(t, db.PingContext(ctx))
	t.Cleanup(func() {
		require.NoError(t, db.Close())
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := admin.ExecContext(cleanup, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err)
		require.NoError(t, admin.Close())
	})
	return db
}

func postgresService(t *testing.T, db *sql.DB, executor Executor) *Service {
	t.Helper()
	cfg := testConfig()
	cfg.ReconcileInterval = 10 * time.Millisecond
	cfg.OperationTimeout = time.Second
	service, err := New(db, executor, cfg, Target{Namespace: "test", InstallationID: "console-test"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, service.Initialize(ctx))
	var schema string
	require.NoError(t, db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema))
	digest := sha256.Sum256([]byte(schema))
	service.lockID = int64(binary.BigEndian.Uint64(digest[:8]) & 0x7fffffffffffffff)
	return service
}

func TestPostgresIntentOwnershipAndFrozenReplay(t *testing.T) {
	db := postgresTestDB(t)
	service := postgresService(t, db, &testExecutor{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, service.Initialize(ctx))
	created, err := service.Create(ctx, "alice", "http-test", "request-a")
	require.NoError(t, err)
	require.Equal(t, StatusPending, created.Status)
	require.Equal(t, DesiredRunning, created.DesiredState)
	require.NotEmpty(t, created.ID)
	require.NotEmpty(t, created.TemplateDigest)

	service.templates["http-test"] = Template{ID: "http-test", Image: "changed-image"}
	replayed, err := service.Create(ctx, "alice", "http-test", "request-a")
	require.NoError(t, err)
	require.Equal(t, created.ID, replayed.ID)
	require.Equal(t, created.Snapshot, replayed.Snapshot)
	delete(service.templates, "http-test")
	replayed, err = service.Create(ctx, "alice", "http-test", "request-a")
	require.NoError(t, err)
	require.Equal(t, created.ID, replayed.ID)
	_, err = service.Create(ctx, "alice", "another-template", "request-a")
	require.ErrorIs(t, err, ErrConflict)
	_, err = service.Create(ctx, "alice", "unknown", "new-request")
	require.ErrorIs(t, err, ErrInvalid)
	_, err = service.Get(ctx, "bob", created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = service.Delete(ctx, "bob", created.ID)
	require.ErrorIs(t, err, ErrNotFound)
	deleted, err := service.Delete(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, DesiredDeleted, deleted.DesiredState)
	require.Equal(t, StatusDeleting, deleted.Status)
	repeated, err := service.Delete(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, deleted.Revision, repeated.Revision)
	replayed, err = service.Create(ctx, "alice", "http-test", "request-a")
	require.NoError(t, err)
	require.Equal(t, DesiredDeleted, replayed.DesiredState)
	list, err := service.List(ctx, "bob")
	require.NoError(t, err)
	require.Empty(t, list)
}

func TestPostgresInitializationPropagatesCanceledContextAndDDLFailure(t *testing.T) {
	db := postgresTestDB(t)
	service, err := New(db, &testExecutor{}, testConfig(), Target{Namespace: "test", InstallationID: "console-test"})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, service.Initialize(ctx), context.Canceled)
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, "CREATE VIEW console_workloads AS SELECT 1 AS id")
	require.NoError(t, err)
	err = service.Initialize(ctx)
	require.ErrorContains(t, err, "initialize workload schema")
	var exists bool
	require.NoError(t, db.QueryRowContext(ctx, "SELECT to_regclass(current_schema() || '.console_workloads') IS NOT NULL").Scan(&exists))
	require.True(t, exists)
}

func TestPostgresKubernetesReceiptWriteFailureRecoversOriginalIdentity(t *testing.T) {
	db := postgresTestDB(t)
	var externalUID string
	var creations int
	executor := &testExecutor{ensure: func(_ context.Context, workload Workload) (Observation, error) {
		if externalUID == "" {
			externalUID = "created-before-receipt"
			creations++
		}
		return Observation{DeploymentUID: externalUID, ServiceUID: "service-original", Ready: true, ObservedAt: time.Now()}, nil
	}}
	service := postgresService(t, db, executor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := service.Create(ctx, "alice", "http-test", "receipt-failure")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, `CREATE FUNCTION reject_workload_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt persistence unavailable'; END $$;
		CREATE TRIGGER reject_workload_receipt BEFORE UPDATE ON console_workloads FOR EACH ROW EXECUTE FUNCTION reject_workload_receipt()`)
	require.NoError(t, err)
	err = service.reconcileNext(ctx)
	require.ErrorContains(t, err, "persist reconciliation observation")
	unrecorded, err := service.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, unrecorded.Status)
	require.Empty(t, unrecorded.DeploymentUID)
	_, err = db.ExecContext(ctx, "DROP TRIGGER reject_workload_receipt ON console_workloads; DROP FUNCTION reject_workload_receipt()")
	require.NoError(t, err)
	restarted := postgresService(t, db, executor)
	require.NoError(t, restarted.reconcileNext(ctx))
	recovered, err := restarted.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusReady, recovered.Status)
	require.Equal(t, externalUID, recovered.DeploymentUID)
	require.Equal(t, 1, creations)
}

func TestPostgresConcurrentIdempotentCreate(t *testing.T) {
	const requests = 16
	db := postgresTestDB(t)
	db.SetMaxOpenConns(requests + 2)
	service := postgresService(t, db, &testExecutor{})
	service.cfg.OperationTimeout = 5 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var group sync.WaitGroup
	blocker, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() {
		rollbackErr := blocker.Rollback()
		cancel()
		group.Wait()
		if !errors.Is(rollbackErr, sql.ErrTxDone) {
			require.NoError(t, rollbackErr)
		}
	}()

	// SHARE permits every preflight SELECT but blocks INSERT's ROW EXCLUSIVE lock.
	// Waiting for all INSERTs forces the losing requests through conflict replay.
	// https://www.postgresql.org/docs/18/explicit-locking.html
	_, err = blocker.ExecContext(ctx, "LOCK TABLE console_workloads IN SHARE MODE")
	require.NoError(t, err)
	results := make(chan Workload, requests)
	errors := make(chan error, requests)
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := service.Create(ctx, "alice", "http-test", "same-key")
			results <- result
			errors <- err
		}()
	}
	waitCtx, stopWaiting := context.WithTimeout(ctx, 4*time.Second)
	defer stopWaiting()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked int
		err := db.QueryRowContext(waitCtx, `SELECT count(*) FROM pg_locks
			WHERE relation = 'console_workloads'::regclass
			AND mode = 'RowExclusiveLock' AND NOT granted`).Scan(&blocked)
		require.NoError(t, err)
		if blocked == requests {
			break
		}
		select {
		case <-waitCtx.Done():
			require.FailNow(t, "workload INSERTs did not all reach the lock barrier", "blocked %d of %d: %v", blocked, requests, waitCtx.Err())
		case <-ticker.C:
		}
	}
	require.NoError(t, blocker.Commit())
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var id string
	for result := range results {
		if id == "" {
			id = result.ID
		}
		require.Equal(t, id, result.ID)
	}
	list, err := service.List(ctx, "alice")
	require.NoError(t, err)
	require.Len(t, list, 1)
	bob, err := service.Create(ctx, "bob", "http-test", "same-key")
	require.NoError(t, err)
	require.NotEqual(t, id, bob.ID)
}

func TestPostgresConcurrentSchemaInitialization(t *testing.T) {
	db := postgresTestDB(t)
	service, err := New(db, &testExecutor{}, testConfig(), Target{Namespace: "test", InstallationID: "console-test"})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const starters = 8
	results := make(chan error, starters)
	var group sync.WaitGroup
	for range starters {
		group.Add(1)
		go func() {
			defer group.Done()
			results <- service.Initialize(ctx)
		}()
	}
	group.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
}

func TestPostgresDeleteSerializesWithRunningCreationAndRetainsTombstone(t *testing.T) {
	db := postgresTestDB(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var creates atomic.Int32
	executor := &testExecutor{
		ensure: func(ctx context.Context, workload Workload) (Observation, error) {
			creates.Add(1)
			close(started)
			select {
			case <-release:
				return Observation{DeploymentUID: "deployment-original", ServiceUID: "service-original", Ready: true, ObservedAt: time.Now()}, nil
			case <-ctx.Done():
				return Observation{}, ctx.Err()
			}
		},
		delete: func(_ context.Context, workload Workload) (Observation, error) {
			require.Equal(t, "deployment-original", workload.DeploymentUID)
			require.Equal(t, "service-original", workload.ServiceUID)
			return Observation{Deleted: true, ObservedAt: time.Now()}, nil
		},
	}
	service := postgresService(t, db, executor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := service.Create(ctx, "alice", "http-test", "race-request")
	require.NoError(t, err)
	reconcileDone := make(chan error, 1)
	go func() { reconcileDone <- service.reconcileNext(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("creation did not start")
	}
	deleteDone := make(chan error, 1)
	go func() { _, err := service.Delete(ctx, "alice", created.ID); deleteDone <- err }()
	select {
	case err := <-deleteDone:
		t.Fatalf("delete returned before creation completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-reconcileDone)
	require.NoError(t, <-deleteDone)
	require.NoError(t, service.reconcileNext(ctx))
	deleted, err := service.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDeleted, deleted.Status)
	require.Equal(t, "deployment-original", deleted.DeploymentUID)
	require.NoError(t, service.reconcileNext(ctx))
	require.Equal(t, int32(1), creates.Load())
	replayed, err := service.Create(ctx, "alice", "http-test", "race-request")
	require.NoError(t, err)
	require.Equal(t, StatusDeleted, replayed.Status)
}

func TestPostgresDeletedTombstoneCleansLateRemoteCreation(t *testing.T) {
	db := postgresTestDB(t)
	var creationAttempts, deletionAttempts int
	var remoteExists bool
	executor := &testExecutor{
		ensure: func(_ context.Context, _ Workload) (Observation, error) {
			creationAttempts++
			// The client loses the receipt while the remote create remains pending.
			return Observation{}, context.DeadlineExceeded
		},
		delete: func(_ context.Context, workload Workload) (Observation, error) {
			deletionAttempts++
			if remoteExists {
				remoteExists = false
				return Observation{DeploymentUID: "late-deployment", ServiceUID: "late-service", Deleted: false, ObservedAt: time.Now()}, nil
			}
			return Observation{Deleted: true, ObservedAt: time.Now()}, nil
		},
	}
	service := postgresService(t, db, executor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := service.Create(ctx, "alice", "http-test", "uncertain-remote-create")
	require.NoError(t, err)
	require.NoError(t, service.reconcileNext(ctx))
	_, err = service.Delete(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.NoError(t, service.reconcileNext(ctx))
	deleted, err := service.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDeleted, deleted.Status)
	remoteExists = true
	require.NoError(t, service.reconcileNext(ctx))
	cleaning, err := service.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDeleting, cleaning.Status)
	require.Equal(t, "late-deployment", cleaning.DeploymentUID)
	require.False(t, remoteExists)
	require.NoError(t, service.reconcileNext(ctx))
	deleted, err = service.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusDeleted, deleted.Status)
	require.Equal(t, 1, creationAttempts)
	require.Equal(t, 3, deletionAttempts)
	replay, err := service.Create(ctx, "alice", "http-test", "uncertain-remote-create")
	require.NoError(t, err)
	require.Equal(t, DesiredDeleted, replay.DesiredState)
}

func TestPostgresReconcilePersistsPartialIdentityWithoutSecrets(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(logger)
	db := postgresTestDB(t)
	var attempt int
	executor := &testExecutor{ensure: func(_ context.Context, workload Workload) (Observation, error) {
		attempt++
		if attempt == 1 {
			return Observation{DeploymentUID: "original", ObservedAt: time.Now()}, errors.New("password=sensitive-value")
		}
		require.Equal(t, "original", workload.DeploymentUID)
		return Observation{DeploymentUID: "original", ServiceUID: "service", Ready: true, ObservedAt: time.Now()}, nil
	}}
	service := postgresService(t, db, executor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := service.Create(ctx, "alice", "http-test", "partial")
	require.NoError(t, err)
	require.NoError(t, service.reconcileNext(ctx))
	failed, err := service.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status)
	require.Equal(t, "original", failed.DeploymentUID)
	require.NotContains(t, failed.LastError, "sensitive-value")
	require.NotContains(t, logs.String(), "sensitive-value")
	require.Contains(t, logs.String(), "error_category")
	restarted := postgresService(t, db, executor)
	require.NoError(t, restarted.reconcileNext(ctx))
	ready, err := restarted.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusReady, ready.Status)
	require.Empty(t, ready.LastError)
}

func TestPostgresRepeatedReconcilerShutdownReleasesLease(t *testing.T) {
	db := postgresTestDB(t)
	service := postgresService(t, db, &testExecutor{})
	service.cfg.ReconcileInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for range 100 {
		runContext, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- service.Run(runContext) }()
		require.Eventually(t, func() bool {
			var locked bool
			err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND granted)`, uint32(uint64(service.lockID)>>32), uint32(service.lockID&0xffffffff)).Scan(&locked)
			return err == nil && locked
		}, time.Second, time.Millisecond)
		stop()
		require.NoError(t, <-done)
		var released bool
		err := db.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND granted)`, uint32(uint64(service.lockID)>>32), uint32(service.lockID&0xffffffff)).Scan(&released)
		require.NoError(t, err)
		require.True(t, released)
	}
}

func TestPostgresOnlyOneReconcilerRuns(t *testing.T) {
	db := postgresTestDB(t)
	service := postgresService(t, db, &testExecutor{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstContext, stop := context.WithCancel(ctx)
	defer stop()
	firstDone := make(chan error, 1)
	go func() { firstDone <- service.Run(firstContext) }()
	require.Eventually(t, func() bool {
		var locked bool
		err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND granted)`, uint32(uint64(service.lockID)>>32), uint32(service.lockID&0xffffffff)).Scan(&locked)
		return err == nil && locked
	}, time.Second, 10*time.Millisecond)
	require.ErrorContains(t, service.Run(ctx), "another Console reconciler")
	stop()
	require.NoError(t, <-firstDone)
	finalContext, finalStop := context.WithCancel(ctx)
	defer finalStop()
	finalDone := make(chan error, 1)
	go func() { finalDone <- service.Run(finalContext) }()
	time.Sleep(30 * time.Millisecond)
	finalStop()
	require.NoError(t, <-finalDone)
}

func TestPostgresLeaseLossCancelsInFlightOperation(t *testing.T) {
	db := postgresTestDB(t)
	started := make(chan struct{})
	canceled := make(chan struct{})
	executor := &testExecutor{ensure: func(ctx context.Context, _ Workload) (Observation, error) {
		close(started)
		<-ctx.Done()
		close(canceled)
		return Observation{}, ctx.Err()
	}}
	service := postgresService(t, db, executor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	created, err := service.Create(ctx, "alice", "http-test", "lost-lease")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("operation did not start")
	}
	var terminated bool
	err = db.QueryRowContext(ctx, `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND classid=$1 AND objid=$2 AND granted`, uint32(uint64(service.lockID)>>32), uint32(service.lockID&0xffffffff)).Scan(&terminated)
	require.NoError(t, err)
	require.True(t, terminated)
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("lease loss did not cancel operation")
	}
	require.ErrorContains(t, <-done, "lost PostgreSQL lock session")
	workload, err := service.Get(ctx, "alice", created.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, workload.Status)
}
