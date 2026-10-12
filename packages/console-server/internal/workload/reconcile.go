package workload

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"
)

const reconcilerLock int64 = 0x434f4e534f4c4501

var safeCategory = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

func executionCategory(err error) string {
	var categorized interface{ ErrorCategory() string }
	if errors.As(err, &categorized) {
		if category := categorized.ErrorCategory(); safeCategory.MatchString(category) {
			return category
		}
	}
	return "kubernetes_operation_failed"
}

func (s *Service) Run(ctx context.Context) (result error) {
	if ctx.Err() != nil {
		return nil
	}
	// PostgreSQL can acquire the session lock before the driver returns its
	// result. Finish the bounded acquisition before honoring shutdown so an
	// uncertain acquired lock cannot be returned to the pool.
	leaseContext, leaseCancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.OperationTimeout)
	connection, err := s.db.Conn(leaseContext)
	if err != nil {
		leaseCancel()
		return fmt.Errorf("acquire reconciler connection: %w", err)
	}
	var acquired bool
	var backendID int
	err = connection.QueryRowContext(leaseContext, "SELECT pg_try_advisory_lock($1),pg_backend_pid()", s.lockID).Scan(&acquired, &backendID)
	leaseCancel()
	if err != nil || !acquired {
		if err != nil {
			connection.Raw(func(any) error { return driver.ErrBadConn })
		}
		connection.Close()
		if err != nil {
			return fmt.Errorf("acquire reconciler lock: %w", err)
		}
		return fmt.Errorf("another Console reconciler is active")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		err := connection.QueryRowContext(cleanup, "SELECT pg_advisory_unlock($1)", s.lockID).Scan(&unlocked)
		if err != nil || !unlocked {
			// A session lock must never return to the pool after an uncertain unlock.
			connection.Raw(func(any) error { return driver.ErrBadConn })
			if err != nil && result == nil && !(ctx.Err() != nil && (errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone))) {
				result = fmt.Errorf("release reconciler lock: %w", err)
			}
		}
		connection.Close()
	}()

	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan error, 1)
	go func() {
		err := s.monitorLease(runContext, connection, backendID)
		if err != nil {
			cancel()
		}
		heartbeatDone <- err
	}()
	defer func() {
		cancel()
		if err := <-heartbeatDone; err != nil {
			result = errors.Join(result, err)
		}
	}()

	ticker := time.NewTicker(s.cfg.ReconcileInterval)
	defer ticker.Stop()
	for {
		if err := s.reconcileNext(runContext); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reconcile workload: %w", err)
		}
		select {
		case <-runContext.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Service) monitorLease(ctx context.Context, connection *sql.Conn, backendID int) error {
	interval := min(s.cfg.ReconcileInterval, time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Graceful shutdown waits for this bounded probe instead of canceling
			// its query: lib/pq closes the session on query cancellation, releasing
			// the advisory lock before the orderly unlock can run.
			check, cancel := context.WithTimeout(context.WithoutCancel(ctx), min(s.cfg.OperationTimeout, 5*time.Second))
			var currentID int
			err := connection.QueryRowContext(check, "SELECT pg_backend_pid()").Scan(&currentID)
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				return fmt.Errorf("Console reconciler lost PostgreSQL lock session: %w", err)
			}
			if currentID != backendID {
				return fmt.Errorf("Console reconciler lock session changed")
			}
		}
	}
}

func (s *Service) reconcileNext(ctx context.Context) error {
	operationContext, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout+5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(operationContext, nil)
	if err != nil {
		return fmt.Errorf("begin reconciliation: %w", err)
	}
	defer tx.Rollback()
	workload, err := scanWorkload(tx.QueryRowContext(operationContext, "SELECT "+columns+" FROM console_workloads ORDER BY updated_at,id LIMIT 1 FOR UPDATE SKIP LOCKED"))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock reconciliation intent: %w", err)
	}
	if workload.DesiredState == DesiredDeleted {
		workload.Status = StatusDeleting
	} else {
		workload.Status = StatusProvisioning
	}
	execution, stopExecution := context.WithTimeout(operationContext, s.cfg.OperationTimeout)
	var observation Observation
	var executionError error
	if workload.DesiredState == DesiredDeleted {
		observation, executionError = s.executor.Delete(execution, workload)
	} else {
		observation, executionError = s.executor.Ensure(execution, workload)
	}
	stopExecution()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if executionError != nil {
		phase := "provision"
		if workload.DesiredState == DesiredDeleted {
			phase = "delete"
		}
		slog.ErrorContext(ctx, "Kubernetes workload operation failed", "developer_id", workload.DeveloperID, "workload_id", workload.ID, "phase", phase, "error_category", executionCategory(executionError))
	}
	status, safeError := reconcileStatus(workload, observation, executionError)
	if observation.DeploymentUID == "" {
		observation.DeploymentUID = workload.DeploymentUID
	}
	if observation.ServiceUID == "" {
		observation.ServiceUID = workload.ServiceUID
	}
	if workload.DeploymentUID != "" && observation.DeploymentUID != workload.DeploymentUID || workload.ServiceUID != "" && observation.ServiceUID != workload.ServiceUID {
		return fmt.Errorf("executor attempted to replace a recorded Kubernetes identity")
	}
	var observedAt any
	if !observation.ObservedAt.IsZero() {
		observedAt = observation.ObservedAt
	}
	_, err = tx.ExecContext(operationContext, `UPDATE console_workloads SET status=$2,deployment_uid=$3,service_uid=$4,observed_at=COALESCE($5,observed_at),last_error=$6,updated_at=now() WHERE id=$1`, workload.ID, status, observation.DeploymentUID, observation.ServiceUID, observedAt, safeError)
	if err != nil {
		return fmt.Errorf("persist reconciliation observation: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reconciliation observation: %w", err)
	}
	return nil
}

func reconcileStatus(workload Workload, observation Observation, executionError error) (string, string) {
	if executionError != nil {
		if workload.DesiredState == DesiredDeleted {
			return StatusDeleting, "Kubernetes deletion failed; reconciliation will retry"
		}
		return StatusFailed, "Kubernetes provisioning failed; reconciliation will retry"
	}
	if workload.DesiredState == DesiredDeleted {
		if observation.Deleted {
			return StatusDeleted, ""
		}
		return StatusDeleting, ""
	}
	if observation.Ready {
		return StatusReady, ""
	}
	return StatusProvisioning, ""
}
