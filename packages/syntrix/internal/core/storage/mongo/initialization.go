package mongo

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// IndexDefinition is an owner's complete required index contract. Cleanup indexes
// are applied only after read-path acceptance, independently of normal startup.
type IndexDefinition struct {
	Name               string `json:"name"`
	Keys               bson.D `json:"keys"`
	Unique             bool   `json:"unique"`
	ExpireAfterSeconds *int64 `json:"expire_after_seconds,omitempty"`
}

type IndexObservation struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

var ErrIndexConflict = errors.New("required MongoDB index conflicts with its specification")

type IndexError struct {
	Operation, Index string
	Cause            error
}

func (e *IndexError) Error() string {
	return fmt.Sprintf("MongoDB index %s: %s failed", e.Index, e.Operation)
}
func (e *IndexError) Unwrap() error { return e.Cause }

func DocumentIndexDefinitions() (required, cleanup []IndexDefinition) {
	return []IndexDefinition{{Name: "database_1_collection_hash_1", Keys: bson.D{{Key: "database", Value: 1}, {Key: "collection_hash", Value: 1}}}},
		[]IndexDefinition{{Name: "sys_expires_at_1", Keys: bson.D{{Key: "sys_expires_at", Value: 1}}, ExpireAfterSeconds: seconds(0)}}
}

func seconds(n int64) *int64 { return &n }

func InspectIndexDefinitions(ctx context.Context, coll *mongo.Collection, definitions []IndexDefinition) ([]IndexObservation, error) {
	if len(definitions) == 0 {
		return []IndexObservation{}, nil
	}
	cursor, err := coll.Indexes().List(ctx)
	if err != nil {
		var command mongo.CommandError
		if errors.As(err, &command) && command.Code == 26 {
			observations := make([]IndexObservation, len(definitions))
			for i, d := range definitions {
				observations[i] = IndexObservation{d.Name, "missing"}
			}
			return observations, nil
		}
		return nil, &IndexError{Operation: "inspect", Cause: err}
	}
	defer cursor.Close(ctx)
	var indexes []bson.Raw
	if err := cursor.All(ctx, &indexes); err != nil {
		return nil, &IndexError{Operation: "read", Cause: err}
	}
	observations := make([]IndexObservation, 0, len(definitions))
	for _, d := range definitions {
		state := "missing"
		for _, raw := range indexes {
			var observed bson.M
			var ordered struct {
				Keys bson.D `bson:"key"`
			}
			if err := bson.Unmarshal(raw, &observed); err != nil {
				return nil, &IndexError{Operation: "decode", Index: d.Name, Cause: err}
			}
			if err := bson.Unmarshal(raw, &ordered); err != nil {
				return nil, &IndexError{Operation: "decode", Index: d.Name, Cause: err}
			}
			keyMatches := len(ordered.Keys) == len(d.Keys)
			if keyMatches {
				for i, key := range d.Keys {
					if ordered.Keys[i].Key != key.Key || !equalIndexValue(ordered.Keys[i].Value, key.Value) {
						keyMatches = false
					}
				}
			}
			if observed["name"] != d.Name && !keyMatches {
				continue
			}
			if observed["name"] != d.Name || !keyMatches || !matchingIndexOptions(observed, d) {
				state = "conflict"
				break
			}
			state = "present"
		}
		observations = append(observations, IndexObservation{d.Name, state})
	}
	return observations, nil
}

func equalIndexValue(a, b any) bool {
	toInt := func(v any) (int64, bool) {
		switch n := v.(type) {
		case int:
			return int64(n), true
		case int32:
			return int64(n), true
		case int64:
			return n, true
		case float64:
			return int64(n), n == float64(int64(n))
		}
		return 0, false
	}
	x, xok := toInt(a)
	y, yok := toInt(b)
	if xok && yok {
		return x == y
	}
	return reflect.DeepEqual(a, b)
}

func matchingIndexOptions(observed bson.M, d IndexDefinition) bool {
	if unique, _ := observed["unique"].(bool); unique != d.Unique {
		return false
	}
	if sparse, _ := observed["sparse"].(bool); sparse {
		return false
	}
	if hidden, _ := observed["hidden"].(bool); hidden {
		return false
	}
	if _, ok := observed["partialFilterExpression"]; ok {
		return false
	}
	if collation, ok := observed["collation"].(bson.M); ok && collation["locale"] != "simple" {
		return false
	}
	ttl, exists := observed["expireAfterSeconds"]
	return (d.ExpireAfterSeconds == nil && !exists) || (d.ExpireAfterSeconds != nil && exists && equalIndexValue(ttl, *d.ExpireAfterSeconds))
}

func EnsureIndexDefinitions(ctx context.Context, coll *mongo.Collection, definitions []IndexDefinition) error {
	observed, err := InspectIndexDefinitions(ctx, coll, definitions)
	if err != nil {
		return err
	}
	for _, observation := range observed {
		if observation.State == "conflict" {
			return &IndexError{Operation: "verify", Index: observation.Name, Cause: ErrIndexConflict}
		}
	}
	for i, d := range definitions {
		if observed[i].State == "present" {
			continue
		}
		opts := options.Index().SetName(d.Name).SetUnique(d.Unique).SetCollation(&options.Collation{Locale: "simple"})
		if d.ExpireAfterSeconds != nil {
			opts.SetExpireAfterSeconds(int32(*d.ExpireAfterSeconds))
		}
		if _, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: d.Keys, Options: opts}); err != nil {
			return &IndexError{Operation: "create", Index: d.Name, Cause: err}
		}
	}
	observed, err = InspectIndexDefinitions(ctx, coll, definitions)
	if err != nil {
		return err
	}
	for _, o := range observed {
		if o.State != "present" {
			return &IndexError{Operation: "verify", Index: o.Name, Cause: ErrIndexConflict}
		}
	}
	return nil
}
