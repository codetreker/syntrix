package workload

import (
	"context"
	"errors"
	"time"

	"github.com/codetreker/syntrix/console-server/internal/workload/types"
)

type Template = types.Template

var (
	ErrNotFound = errors.New("workload not found")
	ErrConflict = errors.New("workload request conflicts with an existing request")
	ErrInvalid  = errors.New("invalid workload request")
)

const (
	DesiredRunning     = "running"
	DesiredDeleted     = "deleted"
	StatusPending      = "pending"
	StatusProvisioning = "provisioning"
	StatusReady        = "ready"
	StatusFailed       = "failed"
	StatusDeleting     = "deleting"
	StatusDeleted      = "deleted"
)

type Target struct {
	Namespace      string
	InstallationID string
}

type Workload struct {
	ID             string     `json:"id"`
	DeveloperID    string     `json:"developer_id"`
	TemplateID     string     `json:"template_id"`
	Snapshot       Template   `json:"-"`
	TemplateDigest string     `json:"-"`
	Namespace      string     `json:"namespace"`
	InstallationID string     `json:"-"`
	DesiredState   string     `json:"desired_state"`
	Status         string     `json:"status"`
	Revision       int64      `json:"revision"`
	DeploymentName string     `json:"deployment_name"`
	ServiceName    string     `json:"service_name"`
	DeploymentUID  string     `json:"deployment_uid,omitempty"`
	ServiceUID     string     `json:"service_uid,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	ObservedAt     *time.Time `json:"observed_at,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
}

type Observation struct {
	DeploymentUID string
	ServiceUID    string
	Ready         bool
	Deleted       bool
	Reason        string
	ObservedAt    time.Time
}

type Executor interface {
	Ensure(context.Context, Workload) (Observation, error)
	Delete(context.Context, Workload) (Observation, error)
}

type TemplateSummary struct {
	ID   string `json:"id"`
	Port int32  `json:"port"`
}
