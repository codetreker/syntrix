package mongo

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

const maxWatchCheckpointSize = 64 * 1024

type watchSource struct {
	Database   string `json:"database"`
	Collection string `json:"collection"`
	UUID       string `json:"uuid"`
}

type watchCheckpoint struct {
	Version       int                  `json:"version"`
	Source        watchSource          `json:"source"`
	Database      string               `json:"database"`
	Collection    string               `json:"collection"`
	IncludeBefore bool                 `json:"includeBefore"`
	Token         bson.Raw             `json:"token,omitempty"`
	Start         *primitive.Timestamp `json:"start,omitempty"`
	ClusterTime   bson.Raw             `json:"clusterTime,omitempty"`
	Target        bson.Raw             `json:"target,omitempty"`
}

func (c watchCheckpoint) encode(token bson.Raw) (types.WatchCheckpoint, error) {
	if err := validateWatchToken(token); err != nil {
		return "", err
	}
	c.Token = token
	c.Start = nil
	c.ClusterTime = nil
	return c.marshal()
}

func (c watchCheckpoint) marshal() (types.WatchCheckpoint, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > maxWatchCheckpointSize {
		return "", errors.New("checkpoint exceeds size limit")
	}
	return types.WatchCheckpoint(encoded), nil
}

func decodeWatchCheckpoint(encoded types.WatchCheckpoint) (watchCheckpoint, error) {
	var c watchCheckpoint
	if len(encoded) == 0 || len(encoded) > maxWatchCheckpointSize {
		return c, errors.New("checkpoint size is invalid")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(string(encoded))
	if err != nil {
		return c, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, err
	}
	// The private encoding is canonical: this also rejects omitted and duplicate
	// fields, trailing input, and alternate interpretations of the same envelope.
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(canonical, data) {
		return c, errors.New("checkpoint envelope is not canonical")
	}
	return c, c.validate()
}

func (c watchCheckpoint) validate() error {
	uuid, err := hex.DecodeString(c.Source.UUID)
	if c.Version != 2 || c.Database == "" || c.Source.Database == "" || c.Source.Collection == "" || err != nil || len(uuid) != 16 {
		return errors.New("checkpoint binding is invalid")
	}
	if len(c.Target) != 0 {
		if err := validateWatchToken(c.Target); err != nil {
			return err
		}
		if _, err := watchTokenOrderKey(c.Target); err != nil {
			return err
		}
	}
	if c.Start != nil {
		return validateWatchBootstrap(c)
	}
	if len(c.ClusterTime) != 0 {
		return errors.New("token checkpoint contains scan context")
	}
	return validateWatchToken(c.Token)
}

func watchTokenOrderKey(token bson.Raw) (string, error) {
	key, ok := token.Lookup("_data").StringValueOK()
	if !ok || key == "" {
		return "", errors.New("source resume token has no ordered data field")
	}
	return key, nil
}

func validateWatchBootstrap(c watchCheckpoint) error {
	if c.Start == nil || c.Start.T == 0 || len(c.Token) != 0 {
		return errors.New("checkpoint is not a scan boundary")
	}
	if err := c.ClusterTime.Validate(); err != nil {
		return errors.New("invalid causal cluster time")
	}
	clock, ok := c.ClusterTime.Lookup("$clusterTime").DocumentOK()
	if !ok {
		return errors.New("causal context lacks cluster time")
	}
	t, i, ok := clock.Lookup("clusterTime").TimestampOK()
	if !ok || t < c.Start.T || t == c.Start.T && i < c.Start.I {
		return errors.New("causal cluster time precedes scan boundary")
	}
	if _, ok := clock.Lookup("signature").DocumentOK(); !ok {
		return errors.New("causal cluster time lacks signature")
	}
	return nil
}

func validateWatchToken(token bson.Raw) error {
	if len(token) > maxWatchCheckpointSize {
		return errors.New("resume token exceeds size limit")
	}
	if len(token) < 5 || binary.LittleEndian.Uint32(token[:4]) != uint32(len(token)) {
		return errors.New("resume token document length is invalid")
	}
	if err := token.Validate(); err != nil {
		return fmt.Errorf("invalid BSON resume token: %w", err)
	}
	elements, err := token.Elements()
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(elements))
	for _, element := range elements {
		if seen[element.Key()] {
			return errors.New("resume token contains duplicate fields")
		}
		seen[element.Key()] = true
	}
	if len(elements) == 0 {
		return errors.New("resume token is empty")
	}
	return nil
}

func (s watchSource) changeID(token bson.Raw) string {
	// Native tokens identify operations within a collection incarnation. Length
	// delimiters prevent ambiguous concatenation of namespace components.
	identity := fmt.Sprintf("%d:%s%d:%s%s", len(s.Database), s.Database, len(s.Collection), s.Collection, s.UUID)
	hash := sha256.New()
	hash.Write([]byte(identity))
	hash.Write(token)
	return "mongo:" + hex.EncodeToString(hash.Sum(nil))
}
