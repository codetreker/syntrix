package realtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

const (
	ReplicaDataMode      = "replica-data"
	TypeReplicaChanged   = "replica_changed"
	TypeReplicaRead      = "replica_read"
	TypeReplicaPage      = "replica_page"
	TypeReplicaAck       = "replica_ack"
	replicaEnvelopeBytes = 1024
	replicaInputBytes    = querycore.MaxPullRequestBytes + replicaEnvelopeBytes
	replicaIDBytes       = 128
)

type replicaAuthPayload struct {
	Token    string `json:"token"`
	Database string `json:"database"`
	Mode     string `json:"mode"`
}
type replicaSubscribePayload struct {
	Collection               string          `json:"collection"`
	Source                   json.RawMessage `json:"source"`
	ExpectedDatabaseIdentity *string         `json:"expectedDatabaseIdentity,omitempty"`
}
type replicaReadPayload struct {
	SubID                    string          `json:"subId"`
	RequestID                string          `json:"requestId"`
	Request                  json.RawMessage `json:"request"`
	ExpectedDatabaseIdentity *string         `json:"expectedDatabaseIdentity,omitempty"`
	ExpectedSourceHash       *string         `json:"expectedSourceHash,omitempty"`
}
type replicaAckPayload struct {
	SubID     string `json:"subId"`
	RequestID string `json:"requestId"`
}
type replicaUnsubscribePayload struct {
	SubID string `json:"subId"`
}
type replicaErrorPayload struct {
	SubID      string `json:"subId,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	RetryAfter int    `json:"retryAfter,omitempty"`
}

func replicaText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.IndexFunc(value, unicode.IsControl) < 0
}

// Scope-bearing envelopes reject duplicate fields instead of choosing whichever
// duplicate happened to be decoded last. Source/request internals use Pull's codec.
func decodeReplicaObject(data []byte, destination any) error {
	if err := model.ValidateJSONUnicode(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("replica payload must be an object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return errors.New("duplicate replica envelope field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("replica payload must contain one object")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func decodeReplicaEnvelope(data []byte) (BaseMessage, error) {
	var message BaseMessage
	if len(data) > replicaInputBytes {
		return message, errors.New("replica frame exceeds request budget")
	}
	if err := decodeReplicaObject(data, &message); err != nil {
		return message, err
	}
	if !replicaText(message.ID, replicaIDBytes) || len(message.Payload) == 0 {
		return message, errors.New("replica frame requires a correlation ID and payload")
	}
	switch message.Type {
	case TypeAuth, TypeSubscribe, TypeUnsubscribe, TypeReplicaRead, TypeReplicaAck:
	default:
		return message, errors.New("unknown replica message type")
	}
	return message, nil
}

func decodeReplicaRead(message BaseMessage, frameBytes int) (replicaReadPayload, storage.ReplicationPullRequest, error) {
	var payload replicaReadPayload
	var request storage.ReplicationPullRequest
	if err := decodeReplicaObject(message.Payload, &payload); err != nil {
		return payload, request, err
	}
	if len(payload.Request) > querycore.MaxPullRequestBytes {
		return payload, request, &http.MaxBytesError{Limit: querycore.MaxPullRequestBytes}
	}
	if frameBytes-len(payload.Request) > replicaEnvelopeBytes {
		return payload, request, &http.MaxBytesError{Limit: replicaEnvelopeBytes}
	}
	if !replicaText(payload.SubID, replicaIDBytes) || payload.RequestID != message.ID || !replicaText(payload.RequestID, replicaIDBytes) || len(payload.Request) == 0 {
		return payload, request, &replication.Failure{Status: http.StatusBadRequest, Code: "REPLICATION_PROTOCOL_ERROR", Message: "Invalid replica correlation"}
	}
	request, err := replication.DecodePullRequest(bytes.NewReader(payload.Request))
	if err != nil {
		return payload, request, err
	}
	if request.Source == nil {
		return payload, request, &replication.Failure{Status: http.StatusBadRequest, Code: "INVALID_REPLICATION_SOURCE", Message: "Replica reads require a query source"}
	}
	if request.RequestID != nil && *request.RequestID != payload.RequestID {
		return payload, request, &replication.Failure{Status: http.StatusBadRequest, Code: "REPLICATION_PROTOCOL_ERROR", Message: "Window request correlation differs"}
	}
	return payload, request, nil
}

func decodeReplicaSource(payload replicaSubscribePayload, subID string) (storage.ReplicationPullRequest, error) {
	request := storage.ReplicationPullRequest{Collection: payload.Collection}
	if !replicaText(payload.Collection, querycore.MaxPullRequestBytes) || len(payload.Source) == 0 {
		return request, errors.New("replica subscription requires a collection and source")
	}
	source, err := wire.DecodeJSONPullSource(payload.Source)
	if err != nil {
		return request, err
	}
	request.Source = source
	if source.Limit != nil {
		request.RequestID = &subID
	}
	return request, nil
}

func encodeReplicaFrame(id, kind string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(BaseMessage{ID: id, Type: kind, Payload: raw})
}
