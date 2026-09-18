package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

func invalidSource(message string) error {
	return fmt.Errorf("%w: %s", types.ErrInvalidReplicationSource, message)
}

// ValidatePullSourceJSON rejects duplicate fields even inside typed object values.
// encoding/json alone accepts duplicates and replaces invalid string encodings.
func ValidatePullSourceJSON(data []byte) error {
	if err := model.ValidateJSONUnicode(data); err != nil {
		return invalidSource("invalid JSON Unicode")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var visit func() error
	visit = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return invalidSource("duplicate field")
				}
				seen[name] = true
				if err := visit(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := visit(); err != nil {
					return err
				}
			}
		default:
			return invalidSource("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := visit(); err != nil {
		return invalidSource("invalid or duplicate JSON field")
	}
	if _, err := d.Token(); err != io.EOF {
		return invalidSource("expected one JSON value")
	}
	return nil
}

func exactJSONFields(data []byte, allowed ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return invalidSource("expected object")
	}
	for key := range fields {
		found := false
		for _, a := range allowed {
			found = found || key == a
		}
		if !found {
			return invalidSource("unknown field")
		}
	}
	return nil
}

func DecodeJSONPullSource(data []byte) (*types.ReplicationSource, error) {
	if err := ValidatePullSourceJSON(data); err != nil {
		return nil, err
	}
	if err := exactJSONFields(data, "version", "filters", "orderBy", "limit"); err != nil {
		return nil, err
	}
	var raw struct {
		Version *int            `json:"version"`
		Filters json.RawMessage `json:"filters"`
		OrderBy json.RawMessage `json:"orderBy"`
		Limit   json.RawMessage `json:"limit"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&raw); err != nil {
		return nil, invalidSource("invalid source fields")
	}
	if raw.Version == nil || *raw.Version != 1 || len(raw.Filters) == 0 || raw.Filters[0] != '[' {
		return nil, invalidSource("version 1 and filters array are required")
	}
	var filterObjects []json.RawMessage
	if err := json.Unmarshal(raw.Filters, &filterObjects); err != nil {
		return nil, invalidSource("invalid filters")
	}
	for _, f := range filterObjects {
		if err := exactJSONFields(f, "field", "op", "value"); err != nil {
			return nil, err
		}
	}
	var filters []struct {
		Field *string         `json:"field"`
		Op    *string         `json:"op"`
		Value json.RawMessage `json:"value"`
	}
	d = json.NewDecoder(bytes.NewReader(raw.Filters))
	d.DisallowUnknownFields()
	if err := d.Decode(&filters); err != nil {
		return nil, invalidSource("invalid filters")
	}
	out := &types.ReplicationSource{Version: 1, Filters: make(model.Filters, 0, len(filters))}
	for _, f := range filters {
		if f.Field == nil || f.Op == nil || len(f.Value) == 0 {
			return nil, invalidSource("incomplete filter")
		}
		value, err := model.DecodeTypedValue(f.Value)
		if err != nil {
			return nil, invalidSource("invalid typed filter value")
		}
		out.Filters = append(out.Filters, model.Filter{Field: *f.Field, Op: model.FilterOp(*f.Op), Value: value})
	}
	if len(raw.OrderBy) > 0 {
		if raw.OrderBy[0] != '[' {
			return nil, invalidSource("orderBy must be an array")
		}
		var orderObjects []json.RawMessage
		if err := json.Unmarshal(raw.OrderBy, &orderObjects); err != nil {
			return nil, invalidSource("invalid orderBy")
		}
		for _, o := range orderObjects {
			if err := exactJSONFields(o, "field", "direction"); err != nil {
				return nil, err
			}
		}
		var orders []struct {
			Field     *string `json:"field"`
			Direction *string `json:"direction"`
		}
		d = json.NewDecoder(bytes.NewReader(raw.OrderBy))
		d.DisallowUnknownFields()
		if err := d.Decode(&orders); err != nil {
			return nil, invalidSource("invalid orderBy")
		}
		for _, o := range orders {
			if o.Field == nil || o.Direction == nil {
				return nil, invalidSource("incomplete order")
			}
			out.OrderBy = append(out.OrderBy, model.Order{Field: *o.Field, Direction: *o.Direction})
		}
	}
	if len(raw.Limit) > 0 {
		var limit int
		if bytes.Equal(raw.Limit, []byte("null")) || json.Unmarshal(raw.Limit, &limit) != nil || limit < 1 || limit > MaxPullLimit {
			return nil, invalidSource("invalid result limit")
		}
		out.Limit = &limit
	}
	return out, nil
}

func EncodePullRequest(database string, req types.ReplicationPullRequest) (*pb.PullRequest, error) {
	out := &pb.PullRequest{Database: database, DatabaseIdentity: req.DatabaseIdentity, Collection: req.Collection, Checkpoint: req.Checkpoint, Limit: int32(req.Limit), WireVersion: Version, RequestId: req.RequestID}
	if req.Limit < 0 || req.Limit > MaxPullLimit {
		return nil, invalidSource("invalid page limit")
	}
	if req.Source != nil {
		s := req.Source
		if s.Version != 1 || s.Filters == nil {
			return nil, invalidSource("version 1 and filters are required")
		}
		q, err := EncodeQuery(model.Query{Filters: s.Filters, OrderBy: s.OrderBy})
		if err != nil {
			return nil, invalidSource("invalid source query")
		}
		out.Source = &pb.ReplicationSource{Version: 1, Filters: q.Filters, OrderBy: q.OrderBy}
		if s.Limit != nil {
			if *s.Limit < 1 || *s.Limit > MaxPullLimit {
				return nil, invalidSource("invalid result limit")
			}
			n := int32(*s.Limit)
			out.Source.Limit = &n
		}
	}
	return out, nil
}

func DecodePullRequest(in *pb.PullRequest) (types.ReplicationPullRequest, error) {
	if in == nil || in.WireVersion != Version {
		return types.ReplicationPullRequest{}, pullFailure(types.WatchInvalidCheckpoint, "unsupported pull request version")
	}
	out := types.ReplicationPullRequest{DatabaseIdentity: in.DatabaseIdentity, Collection: in.Collection, Checkpoint: in.Checkpoint, Limit: int(in.Limit), RequestID: in.RequestId}
	if in.Source != nil {
		s := in.Source
		if s.Version != 1 {
			return out, invalidSource("unsupported source version")
		}
		for _, f := range s.Filters {
			if f == nil {
				return out, invalidSource("missing filter")
			}
			if err := ValidatePullSourceJSON(f.Value); err != nil {
				return out, err
			}
		}
		q, err := DecodeQuery(&pb.Query{Filters: s.Filters, OrderBy: s.OrderBy})
		if err != nil {
			return out, invalidSource("invalid source query")
		}
		out.Source = &types.ReplicationSource{Version: 1, Filters: make(model.Filters, 0, len(q.Filters)), OrderBy: q.OrderBy}
		out.Source.Filters = append(out.Source.Filters, q.Filters...)
		if s.Limit != nil {
			n := int(*s.Limit)
			if n < 1 || n > MaxPullLimit {
				return out, invalidSource("invalid result limit")
			}
			out.Source.Limit = &n
		}
	}
	return out, nil
}

type jsonSourcePullEnvelope struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	Mode              string            `json:"mode"`
	DatabaseIdentity  string            `json:"databaseIdentity"`
	SourceHash        string            `json:"sourceHash"`
	Events            []json.RawMessage `json:"events"`
	Checkpoint        string            `json:"checkpoint"`
	GenerationID      string            `json:"generationId"`
	Phase             string            `json:"phase"`
	CaughtUp          bool              `json:"caughtUp"`
	BootstrapComplete bool              `json:"bootstrapComplete"`
}

func sourceEnvelope(page *types.ReplicationPullResponse) jsonSourcePullEnvelope {
	return jsonSourcePullEnvelope{ProtocolVersion: page.ProtocolVersion, Mode: page.Mode, DatabaseIdentity: page.DatabaseIdentity, SourceHash: page.SourceHash, Events: []json.RawMessage{}, Checkpoint: page.Checkpoint, GenerationID: page.GenerationID, Phase: page.Phase, CaughtUp: page.CaughtUp, BootstrapComplete: page.BootstrapComplete}
}
func sourceProtoEnvelope(page *types.ReplicationPullResponse) *pb.PullResponse {
	complete := page.BootstrapComplete
	return &pb.PullResponse{WireVersion: Version, Checkpoint: page.Checkpoint, CaughtUp: page.CaughtUp, ProtocolVersion: int32(page.ProtocolVersion), Mode: page.Mode, DatabaseIdentity: page.DatabaseIdentity, SourceHash: page.SourceHash, GenerationId: page.GenerationID, Phase: page.Phase, BootstrapComplete: &complete}
}

func validateSourceEnvelope(page *types.ReplicationPullResponse) error {
	if page.ProtocolVersion != 1 || page.Mode != "events" || len(page.Documents) != 0 {
		return pullFailure(types.WatchInvalidEvent, "invalid query replication mode")
	}
	for _, s := range []string{page.DatabaseIdentity, page.SourceHash, page.GenerationID} {
		if s == "" || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
			return pullFailure(types.WatchInvalidEvent, "invalid query replication identity")
		}
	}
	switch page.Phase {
	case "scan", "replay":
		if page.BootstrapComplete || page.CaughtUp {
			return pullFailure(types.WatchInvalidEvent, "premature bootstrap completion")
		}
	case "live":
		if !page.BootstrapComplete {
			return pullFailure(types.WatchInvalidEvent, "missing bootstrap completion")
		}
	default:
		return pullFailure(types.WatchInvalidEvent, "invalid query replication phase")
	}
	if len(page.Events) > MaxPullLimit {
		return model.ErrQueryWorkLimit
	}
	return nil
}

func encodePullEvent(event types.ReplicationEvent) (*pb.ReplicationEvent, []byte, error) {
	out := &pb.ReplicationEvent{Type: string(event.Type), Id: event.ID}
	var data []byte
	var err error
	switch event.Type {
	case types.ReplicationUpsert:
		if event.ID != "" {
			return nil, nil, pullFailure(types.WatchInvalidEvent, "upsert has separate identity")
		}
		if err := validatePullDocument(event.Document); err != nil {
			return nil, nil, err
		}
		if deleted, _ := event.Document["deleted"].(bool); deleted {
			return nil, nil, pullFailure(types.WatchInvalidEvent, "upsert is deleted")
		}
		typed, encodeErr := model.EncodeTypedValue(map[string]any(event.Document))
		if encodeErr != nil {
			return nil, nil, pullFailure(types.WatchInvalidEvent, "invalid upsert document")
		}
		out.Document = &pb.Document{Data: typed}
		data, err = json.Marshal(struct {
			Type     string          `json:"type"`
			Document json.RawMessage `json:"document"`
		}{string(event.Type), typed})
	case types.ReplicationLeave, types.ReplicationDelete:
		if event.Document != nil || event.ID == "" || !utf8.ValidString(event.ID) || strings.ContainsAny(event.ID, "/\x00") {
			return nil, nil, pullFailure(types.WatchInvalidEvent, "invalid ID-only replication event")
		}
		data, err = json.Marshal(struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}{string(event.Type), event.ID})
	default:
		return nil, nil, pullFailure(types.WatchInvalidEvent, "invalid replication event type")
	}
	if err != nil {
		return nil, nil, pullFailure(types.WatchInvalidEvent, "cannot encode replication event")
	}
	return out, data, nil
}

// MeasurePullEvent includes the repeated-field protobuf tag and length prefix.
func MeasurePullEvent(event types.ReplicationEvent) (int, int, error) {
	p, j, err := encodePullEvent(event)
	if err != nil {
		return 0, 0, err
	}
	return len(j), proto.Size(&pb.PullResponse{Events: []*pb.ReplicationEvent{p}}), nil
}

func encodeSourcePullPage(page *types.ReplicationPullResponse) (*pb.PullResponse, *jsonSourcePullEnvelope, error) {
	if err := validatePullEnvelope(page); err != nil {
		return nil, nil, err
	}
	out, envelope := sourceProtoEnvelope(page), sourceEnvelope(page)
	jsonBytes, protoBytes := 0, 0
	for _, event := range page.Events {
		p, j, err := encodePullEvent(event)
		if err != nil {
			return nil, nil, err
		}
		jsonBytes += len(j)
		protoBytes += proto.Size(&pb.PullResponse{Events: []*pb.ReplicationEvent{p}})
		if jsonBytes > MaxPageBytes || protoBytes > MaxGRPCBytes {
			return nil, nil, model.ErrQueryWorkLimit
		}
		out.Events = append(out.Events, p)
		envelope.Events = append(envelope.Events, j)
	}
	if err := CheckPullPageSize(page, jsonBytes, protoBytes); err != nil {
		return nil, nil, err
	}
	return out, &envelope, nil
}

func decodeSourcePullPage(in *pb.PullResponse) (*types.ReplicationPullResponse, error) {
	if in.BootstrapComplete == nil || len(in.Documents) != 0 {
		return nil, pullFailure(types.WatchInvalidEvent, "incomplete query replication envelope")
	}
	out := &types.ReplicationPullResponse{ProtocolVersion: int(in.ProtocolVersion), Mode: in.Mode, DatabaseIdentity: in.DatabaseIdentity, SourceHash: in.SourceHash, GenerationID: in.GenerationId, Phase: in.Phase, Checkpoint: in.Checkpoint, CaughtUp: in.CaughtUp, BootstrapComplete: *in.BootstrapComplete, Events: make([]types.ReplicationEvent, 0, len(in.Events))}
	if err := validatePullEnvelope(out); err != nil {
		return nil, err
	}
	for _, event := range in.Events {
		if event == nil {
			return nil, pullFailure(types.WatchInvalidEvent, "missing replication event")
		}
		e := types.ReplicationEvent{Type: types.ReplicationEventType(event.Type), ID: event.Id}
		if event.Document != nil {
			if !proto.Equal(event.Document, &pb.Document{Data: event.Document.Data}) {
				return nil, pullFailure(types.WatchInvalidEvent, "unexpected event document metadata")
			}
			if err := ValidatePullSourceJSON(event.Document.Data); err != nil {
				return nil, pullFailure(types.WatchInvalidEvent, "invalid typed event document")
			}
			v, err := model.DecodeTypedValue(event.Document.Data)
			if err != nil {
				return nil, pullFailure(types.WatchInvalidEvent, "invalid typed event document")
			}
			object, ok := v.(map[string]any)
			if !ok {
				return nil, pullFailure(types.WatchInvalidEvent, "event document is not an object")
			}
			e.Document = model.Document(object)
		}
		if _, _, err := encodePullEvent(e); err != nil {
			return nil, err
		}
		out.Events = append(out.Events, e)
	}
	if _, _, err := encodeSourcePullPage(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidatePullResponseScope prevents an older RPC peer from silently returning a
// collection page for a query source, or applying documents from another scope.
func ValidatePullResponseScope(req types.ReplicationPullRequest, page *types.ReplicationPullResponse) error {
	if page == nil {
		return pullFailure(types.WatchInvalidEvent, "missing pull response")
	}
	if req.Source == nil {
		if page.ProtocolVersion != 0 {
			return pullFailure(types.WatchInvalidEvent, "unexpected source response")
		}
		return nil
	}
	if page.ProtocolVersion != 1 || page.Mode != "events" || page.DatabaseIdentity != req.DatabaseIdentity {
		return pullFailure(types.WatchInvalidEvent, "pull response scope mismatch")
	}
	for _, event := range page.Events {
		if event.Type == types.ReplicationUpsert && event.Document.GetCollection() != req.Collection {
			return pullFailure(types.WatchInvalidEvent, "pull event collection mismatch")
		}
	}
	return nil
}
