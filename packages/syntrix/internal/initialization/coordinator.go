// Package initialization composes owner-defined storage preparation and cleanup
// without starting account services, listeners, or local index storage.
package initialization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	dbpostgres "github.com/codetreker/syntrix/internal/core/database/postgres"
	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	storagemongo "github.com/codetreker/syntrix/internal/core/storage/mongo"
	"github.com/codetreker/syntrix/internal/core/storage/postgres/schema"
	identitypostgres "github.com/codetreker/syntrix/internal/identity/repository/postgres"
	identityruntime "github.com/codetreker/syntrix/internal/identity/runtime"
)

const SpecificationVersion = 1
const (
	Preparing = "preparing"
	Prepared  = "prepared"
	Finalized = "finalized"
	Conflict  = "conflict"
)

var (
	ErrConflict      = errors.New("initialization specification conflicts with existing definitions")
	ErrSpecification = errors.New("initialization specification does not match the expected receipt")
	ErrNotPrepared   = errors.New("initialization preparation is incomplete")
	ErrActiveCleanup = errors.New("fresh initialization requires inactive document cleanup")
)

type Error struct {
	Operation string
	Cause     error
}

func (e *Error) Error() string { return "initialization " + e.Operation + " failed" }
func (e *Error) Unwrap() error { return e.Cause }

type Observation struct {
	Owner      string `json:"owner"`
	Backend    string `json:"backend"`
	Database   string `json:"database,omitempty"`
	Collection string `json:"collection,omitempty"`
	Definition string `json:"definition"`
	Cleanup    bool   `json:"cleanup"`
	State      string `json:"state"`
}

type Receipt struct {
	Version       int           `json:"version"`
	Specification string        `json:"specification"`
	State         string        `json:"state"`
	Observations  []Observation `json:"observations"`
}
type Status = Receipt

type Coordinator struct {
	cfg              storageconfig.Config
	backends         *storage.Backends
	placements       []storage.MongoPlacement
	schemas          []schema.Specification
	specification    string
	postgresDatabase string
	postgresSchema   string
}

func New(ctx context.Context, cfg storageconfig.Config, backends *storage.Backends) (*Coordinator, error) {
	cfg.Backends = maps.Clone(cfg.Backends)
	cfg.Databases = maps.Clone(cfg.Databases)
	placements, err := storage.InitializationPlacements(cfg, backends)
	if err != nil {
		return nil, &Error{"construct", err}
	}
	identityPlacements, err := identityruntime.InitializationPlacements(cfg, backends)
	if err != nil {
		return nil, &Error{"construct", err}
	}
	placements = append(placements, identityPlacements...)
	slices.SortFunc(placements, func(a, b storage.MongoPlacement) int { return comparePlacement(a, b) })
	for i := 1; i < len(placements); i++ {
		a, b := placements[i-1], placements[i]
		if a.Backend == b.Backend && a.Database == b.Database && a.Collection == b.Collection && a.Owner != b.Owner {
			return nil, &Error{"construct", ErrConflict}
		}
	}
	specs := []schema.Specification{dbpostgres.SchemaSpecification(), identitypostgres.SchemaSpecification()}
	var database, namespace string
	if err := backends.PostgresDB().QueryRowContext(ctx, "SELECT current_database(),current_schema()").Scan(&database, &namespace); err != nil {
		return nil, &Error{"namespace", err}
	}
	for i := range specs {
		specs[i].Namespace = namespace
	}
	endpoint, err := storageconfig.PostgresEndpointIdentity(cfg.Backends[cfg.Topology.User.Primary].Postgres.DSN)
	if err != nil {
		return nil, &Error{"namespace", err}
	}
	spec := struct {
		Version                                                             int
		Mongo                                                               []storage.MongoPlacement
		PostgreSQL                                                          []schema.Specification
		PostgresBackend, PostgresEndpoint, PostgresDatabase, PostgresSchema string
	}{SpecificationVersion, placements, specs, cfg.Topology.User.Primary, endpoint, database, namespace}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return nil, &Error{"specification", err}
	}
	hash := sha256.Sum256(encoded)
	return &Coordinator{cfg: cfg, backends: backends, placements: placements, schemas: specs, postgresDatabase: database, postgresSchema: namespace, specification: fmt.Sprintf("%d:sha256:%s", SpecificationVersion, hex.EncodeToString(hash[:]))}, nil
}

func comparePlacement(a, b storage.MongoPlacement) int {
	return strings.Compare(a.Backend+"\x00"+a.Database+"\x00"+a.Collection+"\x00"+a.Owner, b.Backend+"\x00"+b.Database+"\x00"+b.Collection+"\x00"+b.Owner)
}

func (c *Coordinator) Inspect(ctx context.Context) (Status, error) {
	r := Receipt{Version: SpecificationVersion, Specification: c.specification, State: Finalized, Observations: []Observation{}}
	for _, spec := range c.schemas {
		state, err := schema.Inspect(ctx, c.backends.PostgresDB(), spec)
		if err != nil {
			return Receipt{}, &Error{"inspect", err}
		}
		r.Observations = append(r.Observations, Observation{Owner: spec.Table, Backend: c.cfg.Topology.User.Primary, Database: c.postgresDatabase, Collection: c.postgresSchema, Definition: spec.Table, State: state})
	}
	for _, p := range c.placements {
		for _, group := range []struct {
			definitions []storagemongo.IndexDefinition
			cleanup     bool
		}{{p.Required, false}, {p.Cleanup, true}} {
			observed, err := storagemongo.InspectIndexDefinitions(ctx, p.Handle, group.definitions)
			if err != nil {
				return Receipt{}, &Error{"inspect", err}
			}
			for _, o := range observed {
				r.Observations = append(r.Observations, Observation{Owner: p.Owner, Backend: p.Backend, Database: p.Database, Collection: p.Collection, Definition: o.Name, Cleanup: group.cleanup, State: o.State})
			}
		}
	}
	for _, o := range r.Observations {
		if o.State == "conflict" {
			r.State = Conflict
			break
		}
		if o.State == "missing" {
			if !o.Cleanup {
				r.State = Preparing
			} else if r.State == Finalized {
				r.State = Prepared
			}
		}
	}
	return r, nil
}

func (c *Coordinator) PrepareFresh(ctx context.Context) (Receipt, error) {
	observed, err := c.Inspect(ctx)
	if err != nil {
		return Receipt{}, err
	}
	for _, o := range observed.Observations {
		if o.Owner == "documents" && o.Cleanup && o.State != "missing" {
			return Receipt{}, &Error{"fresh prepare", ErrActiveCleanup}
		}
	}
	return c.Prepare(ctx)
}

func (c *Coordinator) Prepare(ctx context.Context) (Receipt, error) {
	observed, err := c.Inspect(ctx)
	if err != nil {
		return Receipt{}, err
	}
	if observed.State == Conflict {
		return Receipt{}, &Error{"prepare", ErrConflict}
	}
	if err := storage.PrepareIn(ctx, c.cfg, c.backends, c.postgresSchema); err != nil {
		return Receipt{}, &Error{"prepare", err}
	}
	if err := identityruntime.PrepareIn(ctx, c.cfg, c.backends, c.postgresSchema); err != nil {
		return Receipt{}, &Error{"prepare", err}
	}
	observed, err = c.Inspect(ctx)
	if err != nil {
		return Receipt{}, err
	}
	if observed.State != Prepared && observed.State != Finalized {
		return Receipt{}, &Error{"verify preparation", ErrNotPrepared}
	}
	return observed, nil
}

func (c *Coordinator) Finalize(ctx context.Context, expected string) (Receipt, error) {
	if expected != c.specification {
		return Receipt{}, &Error{"finalize", ErrSpecification}
	}
	observed, err := c.Inspect(ctx)
	if err != nil {
		return Receipt{}, err
	}
	if observed.State == Conflict {
		return Receipt{}, &Error{"finalize", ErrConflict}
	}
	if observed.State == Preparing {
		return Receipt{}, &Error{"finalize", ErrNotPrepared}
	}
	for _, p := range c.placements {
		if err := storagemongo.EnsureIndexDefinitions(ctx, p.Handle, p.Cleanup); err != nil {
			return Receipt{}, &Error{"finalize", err}
		}
	}
	observed, err = c.Inspect(ctx)
	if err != nil {
		return Receipt{}, err
	}
	if observed.State != Finalized {
		return Receipt{}, &Error{"verify finalization", ErrNotPrepared}
	}
	return observed, nil
}
