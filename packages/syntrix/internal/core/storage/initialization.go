package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strings"

	dbpostgres "github.com/codetreker/syntrix/internal/core/database/postgres"
	"github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/core/storage/mongo"
	driver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

type MongoPlacement struct {
	Owner      string                  `json:"owner"`
	Backend    string                  `json:"backend"`
	Database   string                  `json:"database"`
	Collection string                  `json:"collection"`
	Required   []mongo.IndexDefinition `json:"required"`
	Cleanup    []mongo.IndexDefinition `json:"cleanup"`
	Handle     *driver.Collection      `json:"-"`
}

type PlacementError struct {
	Owner string
	Cause error
}

func (e *PlacementError) Error() string { return "invalid MongoDB placement for " + e.Owner }
func (e *PlacementError) Unwrap() error { return e.Cause }

// MongoPlacements freezes physical identities without including credentials.
// Aliases with equivalent server identities share one collection specification.
func MongoPlacements(cfg config.Config, backends *Backends, owner, collection string, names []string, required, cleanup []mongo.IndexDefinition) ([]MongoPlacement, error) {
	placements := map[string]MongoPlacement{}
	for _, name := range names {
		backend, ok := cfg.Backends[name]
		if !ok || backend.Type != "mongo" {
			return nil, fmt.Errorf("invalid MongoDB placement for %s", owner)
		}
		physical, err := mongoEndpointIdentity(backend.Mongo.URI)
		if err != nil {
			return nil, &PlacementError{Owner: owner, Cause: err}
		}
		client, db, err := backends.GetMongoClient(name)
		if err != nil {
			return nil, err
		}
		key := physical + "\x00" + db + "\x00" + collection
		placements[key] = MongoPlacement{Owner: owner, Backend: physical, Database: db, Collection: collection, Required: required, Cleanup: cleanup, Handle: client.Database(db).Collection(collection)}
	}
	keys := make([]string, 0, len(placements))
	for key := range placements {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]MongoPlacement, 0, len(keys))
	for _, key := range keys {
		result = append(result, placements[key])
	}
	return result, nil
}

func mongoEndpointIdentity(uri string) (string, error) {
	scheme, remainder, ok := strings.Cut(uri, "://")
	if !ok {
		return "", fmt.Errorf("invalid MongoDB URI scheme")
	}
	parseURI := uri
	srvServiceName := ""
	if scheme == connstring.SchemeMongoDBSRV {
		srvServiceName = "mongodb"
		authority, tail, _ := strings.Cut(remainder, "/")
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			authority = authority[at+1:]
		}
		parseURI = "mongodb://" + authority + "/"
		if _, query, ok := strings.Cut(tail, "?"); ok {
			parameters := url.Values{}
			for _, option := range strings.FieldsFunc(query, func(r rune) bool { return r == ';' || r == '&' }) {
				key, value, ok := strings.Cut(option, "=")
				if !ok || key == "" {
					return "", fmt.Errorf("invalid MongoDB URI option")
				}
				key, err := url.QueryUnescape(key)
				if err != nil {
					return "", err
				}
				value, err = url.QueryUnescape(value)
				if err != nil {
					return "", err
				}
				switch strings.ToLower(key) {
				case "replicaset":
					parameters.Set("replicaSet", value)
				case "srvservicename":
					if len(value) < 1 || len(value) > 62 {
						return "", fmt.Errorf("MongoDB SRV service name must be between 1 and 62 characters")
					}
					srvServiceName = strings.ToLower(value)
				}
			}
			if len(parameters) > 0 {
				parseURI += "?" + parameters.Encode()
			}
		}
	}
	// Parsing a non-SRV URI performs no DNS. The configured SRV authority and
	// service identify the endpoint even when its resolved members change.
	parsed, err := connstring.ParseAndValidate(parseURI)
	if err != nil {
		return "", err
	}
	hosts := append([]string(nil), parsed.Hosts...)
	for i := range hosts {
		if !strings.HasPrefix(hosts[i], "/") {
			hosts[i] = strings.ToLower(hosts[i])
		}
	}
	slices.Sort(hosts)
	endpoint := strings.Join(hosts, ",") + "\x00" + parsed.ReplicaSet + "\x00" + scheme
	if scheme == connstring.SchemeMongoDBSRV {
		endpoint += "\x00" + srvServiceName
	}
	hash := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(hash[:]), nil
}

func RoutedMongoNames(cfg config.Config, primary, replica, strategy string) ([]string, error) {
	names := []string{primary}
	switch strategy {
	case "single":
	case "read_write_split":
		names = append(names, replica)
	default:
		return nil, fmt.Errorf("unsupported MongoDB routing strategy: %s", strategy)
	}
	for logical, placement := range cfg.Databases {
		if logical != "default" {
			names = append(names, placement.Backend)
		}
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

func InitializationPlacements(cfg config.Config, backends *Backends) ([]MongoPlacement, error) {
	topology := cfg.Topology.Document
	names, err := RoutedMongoNames(cfg, topology.Primary, topology.Replica, topology.Strategy)
	if err != nil {
		return nil, err
	}
	required, cleanup := mongo.DocumentIndexDefinitions()
	documents, err := MongoPlacements(cfg, backends, "documents", topology.DataCollection, names, required, cleanup)
	if err != nil {
		return nil, err
	}
	sys, err := MongoPlacements(cfg, backends, "sys", topology.SysCollection, names, required, nil)
	if err != nil {
		return nil, err
	}
	return append(documents, sys...), nil
}

// Prepare initializes required storage definitions without activating tombstone
// cleanup. Scanner retains its own lazy source scan index initialization.
func Prepare(ctx context.Context, cfg config.Config, backends *Backends) error {
	return PrepareIn(ctx, cfg, backends, "")
}

func PrepareIn(ctx context.Context, cfg config.Config, backends *Backends, namespace string) error {
	placements, err := InitializationPlacements(cfg, backends)
	if err != nil {
		return err
	}
	if err := dbpostgres.EnsureSchemaIn(ctx, backends.PostgresDB(), namespace); err != nil {
		return err
	}
	for _, p := range placements {
		if err := mongo.EnsureIndexDefinitions(ctx, p.Handle, p.Required); err != nil {
			return err
		}
	}
	return nil
}
