package wire

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

func sourceTestPage() *types.ReplicationPullResponse {
	return &types.ReplicationPullResponse{ProtocolVersion: 1, Mode: "events", DatabaseIdentity: "database-id", SourceHash: strings.Repeat("a", 64), GenerationID: "5dfb6f18-5306-43a9-9552-cd573eb44f13", Phase: "scan", Checkpoint: "opaque", Events: []types.ReplicationEvent{}}
}

func TestPullSourceRequestTypedRoundTrip(t *testing.T) {
	raw := []byte(`{"version":1,"filters":[{"field":"score","op":"eq","value":{"type":"int64","value":"9223372036854775807"}}],"orderBy":[{"field":"score","direction":"desc"}]}`)
	source, err := DecodeJSONPullSource(raw)
	require.NoError(t, err)
	assert.Equal(t, int64(math.MaxInt64), source.Filters[0].Value)
	req := types.ReplicationPullRequest{DatabaseIdentity: "db-id", Collection: "users", Source: source, Limit: 100}
	encoded, err := EncodePullRequest("db-slug", req)
	require.NoError(t, err)
	marshaled, err := proto.Marshal(encoded)
	require.NoError(t, err)
	var received pb.PullRequest
	require.NoError(t, proto.Unmarshal(marshaled, &received))
	decoded, err := DecodePullRequest(&received)
	require.NoError(t, err)
	assert.Equal(t, req, decoded)
	assert.Equal(t, "db-slug", received.Database)
	limit, id := 3, "window-id"
	req.Source.Limit = &limit
	req.RequestID = &id
	req.Limit = 0
	encoded, err = EncodePullRequest("db-slug", req)
	require.NoError(t, err)
	decoded, err = DecodePullRequest(encoded)
	require.NoError(t, err)
	assert.Equal(t, req, decoded)
	require.NotNil(t, decoded.Source.Limit)
	encoded.Source.Limit = proto.Int32(0)
	_, err = DecodePullRequest(encoded)
	require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
	empty, err := DecodeJSONPullSource([]byte(`{"version":1,"filters":[]}`))
	require.NoError(t, err)
	encoded, err = EncodePullRequest("db", types.ReplicationPullRequest{Source: empty})
	require.NoError(t, err)
	decoded, err = DecodePullRequest(encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.Source.Filters)
}

func TestPullSourceStrictJSON(t *testing.T) {
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{"version":1}`, `{"version":null,"filters":[]}`, `{"version":1.0,"filters":[]}`, `{"version":2,"filters":[]}`, `{"version":1,"filters":null}`,
		`{"Version":1,"filters":[]}`, `{"version":1,"filters":[],"extra":true}`, `{"version":1,"version":1,"filters":[]}`, `{"version":1,"filters":[],"\u0066ilters":[]}`,
		`{"version":1,"filters":[null]}`, `{"version":1,"filters":[{"Field":"x","op":"eq","value":{"type":"null"}}]}`,
		`{"version":1,"filters":[{"field":"x","op":"eq"}]}`, `{"version":1,"filters":[{"field":"x","op":"eq","value":3}]}`,
		`{"version":1,"filters":[{"field":"x","op":"eq","value":{"type":"int64","value":"1","value":"2"}}]}`,
		`{"version":1,"filters":[{"field":"x","op":"eq","value":{"type":"object","value":{"a":{"type":"null"},"a":{"type":"null"}}}}]}`,
		`{"version":1,"filters":[{"field":"x","op":"eq","value":{"type":"string","value":"\ud800"}}]}`,
		`{"version":1,"filters":[],"orderBy":null}`, `{"version":1,"filters":[],"orderBy":[null]}`, `{"version":1,"filters":[],"orderBy":[{"field":"id","Direction":"asc"}]}`,
		`{"version":1,"filters":[],"limit":null}`, `{"version":1,"filters":[],"limit":0}`, `{"version":1,"filters":[],"limit":1001}`, `{"version":1,"filters":[],"limit":1.0}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := DecodeJSONPullSource([]byte(raw))
			require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
		})
	}
}

func TestPullSourceRequestRejectsMalformedProto(t *testing.T) {
	for _, source := range []*pb.ReplicationSource{
		{Version: 0}, {Version: 2}, {Version: 1, Filters: []*pb.Filter{nil}}, {Version: 1, Filters: []*pb.Filter{{Field: "x", Op: "eq"}}},
		{Version: 1, Filters: []*pb.Filter{{Field: "x", Op: "eq", Value: []byte(`{"type":"null","type":"null"}`)}}},
		{Version: 1, OrderBy: []*pb.OrderBy{nil}}, {Version: 1, Limit: proto.Int32(-1)},
	} {
		_, err := DecodePullRequest(&pb.PullRequest{WireVersion: Version, Source: source})
		require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
	}
	require.ErrorIs(t, ReplicationStatusToError(ReplicationErrorToStatus(types.ErrInvalidReplicationSource)), types.ErrInvalidReplicationSource)
}

func TestPullSourceEventsRoundTrip(t *testing.T) {
	page := sourceTestPage()
	page.Events = []types.ReplicationEvent{
		{Type: types.ReplicationUpsert, Document: model.Document{"id": "alice", "collection": "users", "version": int64(math.MaxInt64), "nested": []any{int64(math.MinInt64)}}},
		{Type: types.ReplicationLeave, ID: "bob"}, {Type: types.ReplicationDelete, ID: "charlie"},
	}
	encoded, err := EncodePullPage(page)
	require.NoError(t, err)
	data, err := proto.Marshal(encoded)
	require.NoError(t, err)
	var received pb.PullResponse
	require.NoError(t, proto.Unmarshal(data, &received))
	decoded, err := DecodePullPage(&received)
	require.NoError(t, err)
	assert.Equal(t, page, decoded)
	raw, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &envelope))
	assert.NotContains(t, envelope, "documents")
	assert.JSONEq(t, "false", string(envelope["bootstrapComplete"]))
	assert.JSONEq(t, "false", string(envelope["caughtUp"]))
	var events []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(envelope["events"], &events))
	require.Len(t, events, 3)
	assert.Len(t, events[1], 2)
	assert.Len(t, events[2], 2)
	value, err := model.DecodeTypedValue(events[0]["document"])
	require.NoError(t, err)
	assert.Equal(t, map[string]any(page.Events[0].Document), value)
	page.Events = nil
	raw, err = EncodeJSONPullPage(page)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"events":[]`)
}

func TestPullSourceRejectsInvalidEnvelopeOrEvent(t *testing.T) {
	for name, mutate := range map[string]func(*types.ReplicationPullResponse){
		"version":             func(p *types.ReplicationPullResponse) { p.ProtocolVersion = 2 },
		"mode":                func(p *types.ReplicationPullResponse) { p.Mode = "replace" },
		"identity":            func(p *types.ReplicationPullResponse) { p.DatabaseIdentity = "" },
		"hash":                func(p *types.ReplicationPullResponse) { p.SourceHash = "" },
		"generation":          func(p *types.ReplicationPullResponse) { p.GenerationID = "" },
		"phase":               func(p *types.ReplicationPullResponse) { p.Phase = "unknown" },
		"premature caughtup":  func(p *types.ReplicationPullResponse) { p.CaughtUp = true },
		"premature bootstrap": func(p *types.ReplicationPullResponse) { p.BootstrapComplete = true },
		"missing bootstrap":   func(p *types.ReplicationPullResponse) { p.Phase = "live" },
		"documents":           func(p *types.ReplicationPullResponse) { p.Documents = []model.Document{{"id": "x"}} },
	} {
		t.Run(name, func(t *testing.T) {
			p := sourceTestPage()
			mutate(p)
			out, err := EncodePullPage(p)
			requirePullCode(t, err, types.WatchInvalidEvent)
			require.Nil(t, out)
		})
	}
	for _, e := range []types.ReplicationEvent{
		{}, {Type: types.ReplicationUpsert}, {Type: types.ReplicationUpsert, ID: "a", Document: model.Document{"id": "a", "collection": "users"}},
		{Type: types.ReplicationUpsert, Document: model.Document{"id": "a", "collection": "users", "deleted": true}},
		{Type: types.ReplicationUpsert, Document: model.Document{"id": "a", "collection": "users", "score": math.NaN()}},
		{Type: types.ReplicationDelete, ID: "a/b"}, {Type: types.ReplicationLeave}, {Type: types.ReplicationDelete, ID: "a", Document: model.Document{}},
	} {
		p := sourceTestPage()
		p.Events = append(p.Events, e)
		out, err := EncodePullPage(p)
		requirePullCode(t, err, types.WatchInvalidEvent)
		require.Nil(t, out)
	}
	encoded, err := EncodePullPage(sourceTestPage())
	require.NoError(t, err)
	for _, mutate := range []func(*pb.PullResponse){
		func(p *pb.PullResponse) { p.BootstrapComplete = nil }, func(p *pb.PullResponse) { p.ProtocolVersion = 0 },
		func(p *pb.PullResponse) { p.Events = []*pb.ReplicationEvent{nil} },
		func(p *pb.PullResponse) {
			p.Events = []*pb.ReplicationEvent{{Type: "delete", Id: "a", Document: &pb.Document{Data: []byte(`{"type":"object","value":{}}`)}}}
		},
		func(p *pb.PullResponse) {
			p.Events = []*pb.ReplicationEvent{{Type: "upsert", Document: &pb.Document{Id: "physical", Data: []byte(`{"type":"object","value":{}}`)}}}
		},
	} {
		in := proto.Clone(encoded).(*pb.PullResponse)
		mutate(in)
		page, err := DecodePullPage(in)
		requirePullCode(t, err, types.WatchInvalidEvent)
		require.Nil(t, page)
	}
}

func TestPullSourceEnvelopeBudgetIncludesEventsAndCursor(t *testing.T) {
	page := sourceTestPage()
	page.Events = []types.ReplicationEvent{{Type: types.ReplicationUpsert, Document: model.Document{"id": "a", "collection": "users", "payload": ""}}, {Type: types.ReplicationDelete, ID: "b"}}
	initial, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	page.Events[0].Document["payload"] = strings.Repeat("x", MaxPageBytes-len(initial))
	raw, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	require.Len(t, raw, MaxPageBytes)
	encoded, err := EncodePullPage(page)
	require.NoError(t, err)
	_, err = DecodePullPage(encoded)
	require.NoError(t, err)
	j, p := 0, 0
	for _, e := range page.Events {
		ej, ep, err := MeasurePullEvent(e)
		require.NoError(t, err)
		j += ej
		p += ep
	}
	require.NoError(t, CheckPullPageSize(page, j, p))
	require.ErrorIs(t, CheckPullPageSize(page, j, MaxGRPCBytes), model.ErrQueryWorkLimit)
	page.Checkpoint += "x"
	_, err = EncodeJSONPullPage(page)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.ErrorIs(t, CheckPullPageSize(page, j, p), model.ErrQueryWorkLimit)
	encoded.Checkpoint += "x"
	_, err = DecodePullPage(encoded)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
}

func TestPullSourceResponseScope(t *testing.T) {
	req := types.ReplicationPullRequest{DatabaseIdentity: "database-id", Collection: "users", Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}}}
	p := sourceTestPage()
	require.NoError(t, ValidatePullResponseScope(req, p))
	requirePullCode(t, ValidatePullResponseScope(req, &types.ReplicationPullResponse{Checkpoint: "cp"}), types.WatchInvalidEvent)
	p.DatabaseIdentity = "another"
	requirePullCode(t, ValidatePullResponseScope(req, p), types.WatchInvalidEvent)
	p = sourceTestPage()
	p.Events = []types.ReplicationEvent{{Type: types.ReplicationUpsert, Document: model.Document{"id": "a", "collection": "other"}}}
	requirePullCode(t, ValidatePullResponseScope(req, p), types.WatchInvalidEvent)
	requirePullCode(t, ValidatePullResponseScope(types.ReplicationPullRequest{}, sourceTestPage()), types.WatchInvalidEvent)
}

func TestPullSourceEncodingRejectsInvalidLocalRequests(t *testing.T) {
	zero, oversized := 0, MaxPullLimit+1
	for name, req := range map[string]types.ReplicationPullRequest{
		"negative page limit":        {Limit: -1},
		"oversized page limit":       {Limit: MaxPullLimit + 1},
		"unsupported source version": {Source: &types.ReplicationSource{Version: 2, Filters: model.Filters{}}},
		"missing filters":            {Source: &types.ReplicationSource{Version: 1}},
		"non-finite operand":         {Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{{Field: "score", Op: model.OpEq, Value: math.Inf(1)}}}},
		"zero window limit":          {Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}, Limit: &zero}},
		"oversized window limit":     {Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}, Limit: &oversized}},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := EncodePullRequest("database", req)
			require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
			require.Nil(t, encoded, "invalid local requests must not produce a transmissible partial request")
		})
	}
}

func TestPullEventMeasurementRejectsUnserializableCandidate(t *testing.T) {
	event := types.ReplicationEvent{Type: types.ReplicationUpsert, Document: model.Document{
		"id": "alice", "collection": "users", "nested": map[string]any{"score": math.NaN()},
	}}
	jsonBytes, protobufBytes, err := MeasurePullEvent(event)
	requirePullCode(t, err, types.WatchInvalidEvent)
	require.Zero(t, jsonBytes)
	require.Zero(t, protobufBytes)
	page := sourceTestPage()
	page.Events = []types.ReplicationEvent{event}
	encoded, err := EncodePullPage(page)
	requirePullCode(t, err, types.WatchInvalidEvent)
	require.Nil(t, encoded)
}
