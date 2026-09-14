package mongo

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Replication positions carry causal context independently of the native Puller
// and public Watch formats. Start is inclusive and remains fixed during scan.
type replicationCheckpoint struct {
	Version       int                    `json:"version"`
	Source        watchSource            `json:"source"`
	Database      string                 `json:"database"`
	Collection    string                 `json:"collection"`
	Phase         types.ReplicationPhase `json:"phase"`
	AfterID       string                 `json:"afterId"`
	Start         primitive.Timestamp    `json:"start"`
	Token         bson.Raw               `json:"token"`
	OperationTime primitive.Timestamp    `json:"operationTime"`
	ClusterTime   bson.Raw               `json:"clusterTime"`
}

func (c replicationCheckpoint) validate() error {
	uuid, err := hex.DecodeString(c.Source.UUID)
	if c.Version != 1 || err != nil || len(uuid) != 16 || c.Source.Database == "" || c.Source.Collection == "" {
		return errors.New("invalid replication source binding")
	}
	if err := types.ValidateReplicationScope(c.Database, c.Collection); err != nil {
		return err
	}
	if c.Phase != types.ReplicationScan && c.Phase != types.ReplicationChanges {
		return errors.New("invalid replication phase")
	}
	if strings.ContainsAny(c.AfterID, "/\x00") || (c.Phase == types.ReplicationChanges && c.AfterID != "") {
		return errors.New("invalid scan continuation")
	}
	if c.Start.T == 0 || c.OperationTime.T == 0 || timestampBefore(c.OperationTime, c.Start) {
		return errors.New("invalid committed read boundary")
	}
	if len(c.Token) != 0 {
		if c.Phase == types.ReplicationScan {
			return errors.New("scan must retain its inclusive start")
		}
		if err := validateWatchToken(c.Token); err != nil {
			return err
		}
	}
	if err := c.ClusterTime.Validate(); err != nil {
		return errors.New("invalid causal cluster time")
	}
	clock, ok := c.ClusterTime.Lookup("$clusterTime").DocumentOK()
	if !ok {
		return errors.New("causal context lacks cluster time")
	}
	t, i, ok := clock.Lookup("clusterTime").TimestampOK()
	if !ok || timestampBefore(primitive.Timestamp{T: t, I: i}, c.OperationTime) {
		return errors.New("causal cluster time does not cover operation time")
	}
	if _, ok := clock.Lookup("signature").DocumentOK(); !ok {
		return errors.New("causal cluster time lacks signature")
	}
	return nil
}

func timestampBefore(a, b primitive.Timestamp) bool { return a.T < b.T || a.T == b.T && a.I < b.I }

func (c replicationCheckpoint) position() (types.ReplicationPosition, error) {
	if err := c.validate(); err != nil {
		return types.ReplicationPosition{}, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return types.ReplicationPosition{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > types.MaxReplicationCursorBytes {
		return types.ReplicationPosition{}, errors.New("replication cursor exceeds size limit")
	}
	return types.ReplicationPosition{Phase: c.Phase, Opaque: encoded}, nil
}

func decodeReplicationCheckpoint(position types.ReplicationPosition) (replicationCheckpoint, error) {
	var c replicationCheckpoint
	if len(position.Opaque) == 0 || len(position.Opaque) > types.MaxReplicationCursorBytes {
		return c, errors.New("invalid replication cursor size")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(position.Opaque)
	if err != nil {
		return c, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, err
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(raw, canonical) {
		return c, errors.New("replication cursor is not canonical")
	}
	if c.Phase != position.Phase {
		return c, errors.New("replication cursor phase differs")
	}
	return c, c.validate()
}

func (c *replicationCheckpoint) capture(session mongo.Session) error {
	op := session.OperationTime()
	if op == nil || op.T == 0 {
		return errors.New("source supplied no operation time")
	}
	c.OperationTime = *op
	c.ClusterTime = append(bson.Raw(nil), session.ClusterTime()...)
	return nil
}
