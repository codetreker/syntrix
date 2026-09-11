package events

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// WireVersion identifies the lossless event and document envelopes. Older
// payloads cannot recover document paths or integers already rounded by JSON.
const WireVersion = 2

// MaxEventBytes bounds an admitted event after typed encoding. BSON size alone
// cannot bound this representation because every nested value carries a tag.
const MaxEventBytes = 64 << 20

// MaxRPCBytes includes the complete protobuf response and its progress marker.
const MaxRPCBytes = MaxEventBytes + (1 << 20)

var ErrEventTooLarge = errors.New("encoded event exceeds 64 MiB; reduce source data or change the event budget before rebuilding capture")

type eventWire struct {
	Version      int                `json:"wireVersion"`
	EventID      string             `json:"eventId"`
	Database     string             `json:"database"`
	MgoColl      string             `json:"mgoColl"`
	MgoDocID     string             `json:"mgoDocId"`
	OpType       StoreOperationType `json:"opType"`
	FullDocument json.RawMessage    `json:"fullDoc,omitempty"`
	UpdateDesc   json.RawMessage    `json:"updateDesc,omitempty"`
	ClusterTime  ClusterTime        `json:"clusterTime"`
	TxnNumber    *int64             `json:"txnNumber,omitempty"`
	Timestamp    int64              `json:"timestamp"`
	Backend      string             `json:"backend,omitempty"`
}

type documentWire struct {
	WireVersion    int             `json:"wireVersion"`
	ID             string          `json:"id"`
	Database       string          `json:"database"`
	Fullpath       string          `json:"fullpath"`
	Collection     string          `json:"collection"`
	LogicalID      string          `json:"logicalId"`
	CollectionHash string          `json:"collectionHash"`
	Parent         string          `json:"parent"`
	UpdatedAt      int64           `json:"updatedAt"`
	CreatedAt      int64           `json:"createdAt"`
	Version        int64           `json:"version"`
	Data           json.RawMessage `json:"data"`
	Deleted        bool            `json:"deleted"`
}

type updateWire struct {
	Version         int              `json:"wireVersion"`
	UpdatedFields   json.RawMessage  `json:"updatedFields"`
	RemovedFields   []string         `json:"removedFields,omitempty"`
	TruncatedArrays []TruncatedArray `json:"truncatedArrays,omitempty"`
}

// MarshalEvent preserves logical identity and typed values for durable replay.
func MarshalEvent(evt *StoreChangeEvent) ([]byte, error) {
	if evt == nil {
		return nil, fmt.Errorf("event is nil")
	}
	wire := eventEnvelope(evt)
	var err error
	if evt.FullDocument != nil {
		wire.FullDocument, err = MarshalDocument(evt.FullDocument)
		if err != nil {
			return nil, fmt.Errorf("encode full document: %w", err)
		}
	}
	if evt.UpdateDesc != nil {
		wire.UpdateDesc, err = MarshalUpdateDescription(evt.UpdateDesc)
		if err != nil {
			return nil, fmt.Errorf("encode update description: %w", err)
		}
	}
	if err := ValidateEncodedEventSize(evt, wire.FullDocument, wire.UpdateDesc); err != nil {
		return nil, err
	}
	return json.Marshal(wire)
}

func eventEnvelope(evt *StoreChangeEvent) eventWire {
	return eventWire{
		Version:     WireVersion,
		EventID:     evt.EventID,
		Database:    evt.Database,
		MgoColl:     evt.MgoColl,
		MgoDocID:    evt.MgoDocID,
		OpType:      evt.OpType,
		ClusterTime: evt.ClusterTime,
		TxnNumber:   evt.TxnNumber,
		Timestamp:   evt.Timestamp,
		Backend:     evt.Backend,
	}
}

// ValidateEncodedEventSize checks an envelope using already encoded document
// and delta payloads, so RPC conversion uses the durable admission limit.
func ValidateEncodedEventSize(evt *StoreChangeEvent, document, delta []byte) error {
	header, err := json.Marshal(eventEnvelope(evt))
	if err != nil {
		return err
	}
	size := len(header)
	if len(document) > 0 {
		size += len(`,"fullDoc":`) + len(document)
	}
	if len(delta) > 0 {
		size += len(`,"updateDesc":`) + len(delta)
	}
	if size > MaxEventBytes {
		return fmt.Errorf("%w (encoded bytes: %d)", ErrEventTooLarge, size)
	}
	return nil
}

// UnmarshalEvent rejects unversioned and incompatible durable event payloads.
func UnmarshalEvent(data []byte) (*StoreChangeEvent, error) {
	if len(data) > MaxEventBytes {
		return nil, ErrEventTooLarge
	}
	var wire eventWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	if wire.Version != WireVersion {
		return nil, fmt.Errorf("unsupported event wire version %d; offline buffer rebuild required", wire.Version)
	}
	evt := &StoreChangeEvent{
		EventID:     wire.EventID,
		Database:    wire.Database,
		MgoColl:     wire.MgoColl,
		MgoDocID:    wire.MgoDocID,
		OpType:      wire.OpType,
		ClusterTime: wire.ClusterTime,
		TxnNumber:   wire.TxnNumber,
		Timestamp:   wire.Timestamp,
		Backend:     wire.Backend,
	}
	var err error
	if len(wire.FullDocument) != 0 {
		evt.FullDocument, err = UnmarshalDocument(wire.FullDocument)
		if err != nil {
			return nil, fmt.Errorf("decode full document: %w", err)
		}
	}
	if len(wire.UpdateDesc) != 0 {
		evt.UpdateDesc, err = UnmarshalUpdateDescription(wire.UpdateDesc)
		if err != nil {
			return nil, fmt.Errorf("decode update description: %w", err)
		}
	}
	return evt, nil
}

// MarshalDocument uses an internal envelope without changing StoredDoc's public JSON.
func MarshalDocument(doc *storage.StoredDoc) ([]byte, error) {
	identity, err := types.LogicalDocumentID(doc)
	if err != nil {
		return nil, err
	}
	data, err := model.EncodeTypedValue(doc.Data)
	if err != nil {
		return nil, err
	}
	return json.Marshal(documentWire{
		WireVersion:    WireVersion,
		ID:             doc.Id,
		Database:       doc.Database,
		Fullpath:       doc.Fullpath,
		Collection:     doc.Collection,
		LogicalID:      identity,
		CollectionHash: doc.CollectionHash,
		Parent:         doc.Parent,
		UpdatedAt:      doc.UpdatedAt,
		CreatedAt:      doc.CreatedAt,
		Version:        doc.Version,
		Data:           data,
		Deleted:        doc.Deleted,
	})
}

// UnmarshalDocument checks authoritative identity before exposing business data.
func UnmarshalDocument(data []byte) (*storage.StoredDoc, error) {
	var wire documentWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	if wire.WireVersion != WireVersion {
		return nil, fmt.Errorf("unsupported document wire version %d", wire.WireVersion)
	}
	doc := &storage.StoredDoc{
		Id:             wire.ID,
		Database:       wire.Database,
		Fullpath:       wire.Fullpath,
		Collection:     wire.Collection,
		CollectionHash: wire.CollectionHash,
		Parent:         wire.Parent,
		UpdatedAt:      wire.UpdatedAt,
		CreatedAt:      wire.CreatedAt,
		Version:        wire.Version,
		Deleted:        wire.Deleted,
	}
	identity, err := types.LogicalDocumentID(doc)
	if err != nil {
		return nil, err
	}
	if identity != wire.LogicalID {
		return nil, fmt.Errorf("document logical identity disagrees with fullpath")
	}
	doc.Data, err = decodeObject(wire.Data)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// MarshalUpdateDescription preserves numeric types within update deltas.
func MarshalUpdateDescription(desc *UpdateDescription) ([]byte, error) {
	fields, err := model.EncodeTypedValue(desc.UpdatedFields)
	if err != nil {
		return nil, err
	}
	return json.Marshal(updateWire{Version: WireVersion, UpdatedFields: fields, RemovedFields: desc.RemovedFields, TruncatedArrays: desc.TruncatedArrays})
}

// UnmarshalUpdateDescription rejects legacy JSON deltas that lose integer types.
func UnmarshalUpdateDescription(data []byte) (*UpdateDescription, error) {
	var wire updateWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	if wire.Version != WireVersion {
		return nil, fmt.Errorf("unsupported update description wire version %d", wire.Version)
	}
	fields, err := decodeObject(wire.UpdatedFields)
	if err != nil {
		return nil, err
	}
	return &UpdateDescription{UpdatedFields: fields, RemovedFields: wire.RemovedFields, TruncatedArrays: wire.TruncatedArrays}, nil
}

func decodeObject(data []byte) (map[string]any, error) {
	value, err := model.DecodeTypedValue(data)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("document fields must be an object")
	}
	return object, nil
}
