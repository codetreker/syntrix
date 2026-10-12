package workload

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	workloadconfig "github.com/codetreker/syntrix/console-server/internal/workload/config"
)

type Service struct {
	db        *sql.DB
	executor  Executor
	cfg       workloadconfig.Config
	target    Target
	templates map[string]Template
	lockID    int64
}

func New(db *sql.DB, executor Executor, cfg workloadconfig.Config, target Target) (*Service, error) {
	if db == nil || executor == nil || target.Namespace == "" || target.InstallationID == "" {
		return nil, fmt.Errorf("workload service requires PostgreSQL, executor, namespace, and installation identity")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	templates := make(map[string]Template, len(cfg.Templates))
	for _, template := range cfg.Templates {
		template.Env = cloneEnvironment(template.Env)
		templates[template.ID] = template
	}
	return &Service{db: db, executor: executor, cfg: cfg, target: target, templates: templates, lockID: reconcilerLock}, nil
}

func cloneEnvironment(environment map[string]string) map[string]string {
	if environment == nil {
		return nil
	}
	copy := make(map[string]string, len(environment))
	for name, value := range environment {
		copy[name] = value
	}
	return copy
}

func (s *Service) Templates() []TemplateSummary {
	result := make([]TemplateSummary, 0, len(s.templates))
	for _, template := range s.templates {
		result = append(result, TemplateSummary{ID: template.ID, Port: template.Port})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func (s *Service) Initialize(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin workload schema initialization: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock($1)", int64(0x434f4e5343484501)); err != nil {
		return fmt.Errorf("lock workload schema initialization: %w", err)
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS console_workloads (
		id TEXT PRIMARY KEY,
		developer_id TEXT NOT NULL,
		template_id TEXT NOT NULL,
		idempotency_key TEXT NOT NULL,
		snapshot JSONB NOT NULL,
		template_digest TEXT NOT NULL,
		namespace TEXT NOT NULL,
		installation_id TEXT NOT NULL,
		desired_state TEXT NOT NULL CHECK (desired_state IN ('running','deleted')),
		status TEXT NOT NULL CHECK (status IN ('pending','provisioning','ready','failed','deleting','deleted')),
		revision BIGINT NOT NULL DEFAULT 1,
		deployment_name TEXT NOT NULL,
		service_name TEXT NOT NULL,
		deployment_uid TEXT NOT NULL DEFAULT '',
		service_uid TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		observed_at TIMESTAMPTZ,
		last_error TEXT NOT NULL DEFAULT '',
		UNIQUE (developer_id,idempotency_key)
	);
	CREATE INDEX IF NOT EXISTS console_workloads_owner_created ON console_workloads (developer_id,created_at DESC,id DESC);
	CREATE INDEX IF NOT EXISTS console_workloads_reconcile ON console_workloads (updated_at,id);`)
	if err != nil {
		return fmt.Errorf("initialize workload schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit workload schema initialization: %w", err)
	}
	return nil
}

const columns = `id, developer_id, template_id, snapshot, template_digest, namespace, installation_id, desired_state, status, revision, deployment_name, service_name, deployment_uid, service_uid, created_at, updated_at, observed_at, last_error`

type scanner interface{ Scan(...any) error }

func scanWorkload(row scanner) (Workload, error) {
	var workload Workload
	var snapshot []byte
	var observed sql.NullTime
	err := row.Scan(&workload.ID, &workload.DeveloperID, &workload.TemplateID, &snapshot, &workload.TemplateDigest, &workload.Namespace, &workload.InstallationID, &workload.DesiredState, &workload.Status, &workload.Revision, &workload.DeploymentName, &workload.ServiceName, &workload.DeploymentUID, &workload.ServiceUID, &workload.CreatedAt, &workload.UpdatedAt, &observed, &workload.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return Workload{}, ErrNotFound
	}
	if err != nil {
		return Workload{}, err
	}
	if err := json.Unmarshal(snapshot, &workload.Snapshot); err != nil {
		return Workload{}, fmt.Errorf("decode persisted workload template: %w", err)
	}
	if observed.Valid {
		workload.ObservedAt = &observed.Time
	}
	return workload, nil
}

func (s *Service) Create(ctx context.Context, developerID, templateID, key string) (Workload, error) {
	if strings.TrimSpace(developerID) == "" || strings.TrimSpace(templateID) == "" || strings.TrimSpace(key) == "" || len(key) > 200 || strings.ContainsAny(key, "\x00\r\n") {
		return Workload{}, ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout)
	defer cancel()
	// Read the original selector before consulting current templates. Configuration
	// changes must not invalidate a previously accepted idempotent request.
	previous, err := scanWorkload(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM console_workloads WHERE developer_id=$1 AND idempotency_key=$2", developerID, key))
	if err == nil {
		return replay(previous, templateID)
	}
	if !errors.Is(err, ErrNotFound) {
		return Workload{}, fmt.Errorf("read workload request: %w", err)
	}
	template, exists := s.templates[templateID]
	if !exists {
		return Workload{}, ErrInvalid
	}
	snapshot, err := json.Marshal(template)
	if err != nil {
		return Workload{}, fmt.Errorf("serialize workload template: %w", err)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return Workload{}, fmt.Errorf("generate workload identity: %w", err)
	}
	id := hex.EncodeToString(idBytes)
	digest := sha256.Sum256(snapshot)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workload{}, fmt.Errorf("begin workload request: %w", err)
	}
	defer tx.Rollback()
	workload, err := scanWorkload(tx.QueryRowContext(ctx, `INSERT INTO console_workloads
		(id,developer_id,template_id,idempotency_key,snapshot,template_digest,namespace,installation_id,desired_state,status,deployment_name,service_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'running','pending',$9,$9)
		ON CONFLICT (developer_id,idempotency_key) DO NOTHING RETURNING `+columns,
		id, developerID, templateID, key, string(snapshot), hex.EncodeToString(digest[:]), s.target.Namespace, s.target.InstallationID, "w-"+id))
	if errors.Is(err, ErrNotFound) {
		workload, err = scanWorkload(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM console_workloads WHERE developer_id=$1 AND idempotency_key=$2", developerID, key))
		if err == nil {
			workload, err = replay(workload, templateID)
		}
	}
	if err != nil {
		return Workload{}, fmt.Errorf("persist workload request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Workload{}, fmt.Errorf("commit workload request: %w", err)
	}
	return workload, nil
}

func replay(workload Workload, templateID string) (Workload, error) {
	if workload.TemplateID != templateID {
		return Workload{}, ErrConflict
	}
	return workload, nil
}

func (s *Service) Get(ctx context.Context, developerID, id string) (Workload, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout)
	defer cancel()
	workload, err := scanWorkload(s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM console_workloads WHERE developer_id=$1 AND id=$2", developerID, id))
	if err != nil {
		return Workload{}, fmt.Errorf("read workload: %w", err)
	}
	return workload, nil
}

func (s *Service) List(ctx context.Context, developerID string) ([]Workload, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM console_workloads WHERE developer_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100", developerID)
	if err != nil {
		return nil, fmt.Errorf("list workloads: %w", err)
	}
	defer rows.Close()
	result := make([]Workload, 0)
	for rows.Next() {
		workload, err := scanWorkload(rows)
		if err != nil {
			return nil, fmt.Errorf("read workload list: %w", err)
		}
		result = append(result, workload)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read workload list: %w", err)
	}
	return result, nil
}

func (s *Service) Delete(ctx context.Context, developerID, id string) (Workload, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.OperationTimeout+5*time.Second)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Workload{}, fmt.Errorf("begin workload deletion: %w", err)
	}
	defer tx.Rollback()
	workload, err := scanWorkload(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM console_workloads WHERE developer_id=$1 AND id=$2 FOR UPDATE", developerID, id))
	if err != nil {
		return Workload{}, fmt.Errorf("lock workload deletion: %w", err)
	}
	if workload.DesiredState != DesiredDeleted {
		workload, err = scanWorkload(tx.QueryRowContext(ctx, "UPDATE console_workloads SET desired_state='deleted',status='deleting',revision=revision+1,updated_at=now(),last_error='' WHERE id=$1 RETURNING "+columns, workload.ID))
		if err != nil {
			return Workload{}, fmt.Errorf("persist workload deletion: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Workload{}, fmt.Errorf("commit workload deletion: %w", err)
	}
	return workload, nil
}
