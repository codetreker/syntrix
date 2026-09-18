package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func testWindowPage() *types.ReplicationPullResponse {
	return &types.ReplicationPullResponse{ProtocolVersion: 1, Mode: "replace", DatabaseIdentity: "entity", SourceHash: strings.Repeat("a", 64), RequestID: proto.String("request"), GenerationID: "6cff681c-6d4a-45a6-90c1-c11ec3d4685f", Complete: proto.Bool(true), EffectiveOrder: []model.Order{{Field: "id", Direction: "asc"}}, Documents: []model.Document{{"id": "alice", "collection": "users", "version": int64(math.MaxInt64), "payload": ""}}}
}

func TestWindowEnvelopeRoundTrip(t *testing.T) {
	page := testWindowPage()
	encoded, err := EncodePullPage(page)
	require.NoError(t, err)
	require.Nil(t, encoded.BootstrapComplete)
	raw, err := proto.Marshal(encoded)
	require.NoError(t, err)
	var received pb.PullResponse
	require.NoError(t, proto.Unmarshal(raw, &received))
	decoded, err := DecodePullPage(&received)
	require.NoError(t, err)
	require.Equal(t, page, decoded)
	data, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &envelope))
	require.Len(t, envelope, 9)
	for _, key := range []string{"protocolVersion", "mode", "databaseIdentity", "sourceHash", "requestId", "generationId", "complete", "effectiveOrder", "documents"} {
		require.Contains(t, envelope, key)
	}
	var docs []json.RawMessage
	require.NoError(t, json.Unmarshal(envelope["documents"], &docs))
	value, err := model.DecodeTypedValue(docs[0])
	require.NoError(t, err)
	require.Equal(t, map[string]any(page.Documents[0]), value)
	page.Documents = nil
	data, err = EncodeJSONPullPage(page)
	require.NoError(t, err)
	require.Contains(t, string(data), `"documents":[]`)
}

func TestWindowEnvelopeRejectsMalformedAndMixedModes(t *testing.T) {
	for name, mutate := range map[string]func(*types.ReplicationPullResponse){
		"missing complete": func(p *types.ReplicationPullResponse) { p.Complete = nil },
		"false complete":   func(p *types.ReplicationPullResponse) { p.Complete = proto.Bool(false) },
		"missing request":  func(p *types.ReplicationPullResponse) { p.RequestID = nil },
		"empty request":    func(p *types.ReplicationPullResponse) { p.RequestID = proto.String("") },
		"nul request":      func(p *types.ReplicationPullResponse) { p.RequestID = proto.String("a\x00") },
		"invalid utf8":     func(p *types.ReplicationPullResponse) { p.DatabaseIdentity = "\xff" },
		"source hash":      func(p *types.ReplicationPullResponse) { p.SourceHash = "hash" },
		"generation":       func(p *types.ReplicationPullResponse) { p.GenerationID = "generation" },
		"checkpoint":       func(p *types.ReplicationPullResponse) { p.Checkpoint = "progress" },
		"caught up":        func(p *types.ReplicationPullResponse) { p.CaughtUp = true },
		"bootstrap":        func(p *types.ReplicationPullResponse) { p.BootstrapComplete = true },
		"phase":            func(p *types.ReplicationPullResponse) { p.Phase = "live" },
		"events": func(p *types.ReplicationPullResponse) {
			p.Events = []types.ReplicationEvent{{Type: types.ReplicationDelete, ID: "alice"}}
		},
		"wrong version": func(p *types.ReplicationPullResponse) { p.ProtocolVersion = 0 },
		"no order":      func(p *types.ReplicationPullResponse) { p.EffectiveOrder = nil },
		"bad direction": func(p *types.ReplicationPullResponse) { p.EffectiveOrder[0].Direction = "up" },
		"empty field":   func(p *types.ReplicationPullResponse) { p.EffectiveOrder[0].Field = "" },
		"duplicate order": func(p *types.ReplicationPullResponse) {
			p.EffectiveOrder = append(p.EffectiveOrder, p.EffectiveOrder[0])
		},
		"tombstone":             func(p *types.ReplicationPullResponse) { p.Documents[0]["deleted"] = true },
		"bad deletion":          func(p *types.ReplicationPullResponse) { p.Documents[0]["deleted"] = "yes" },
		"duplicate id":          func(p *types.ReplicationPullResponse) { p.Documents = append(p.Documents, p.Documents[0]) },
		"invalid identity":      func(p *types.ReplicationPullResponse) { p.Documents[0]["id"] = "a/b" },
		"invalid document utf8": func(p *types.ReplicationPullResponse) { p.Documents[0]["id"] = "\xff" },
		"invalid payload":       func(p *types.ReplicationPullResponse) { p.Documents[0]["payload"] = math.Inf(1) },
	} {
		t.Run(name, func(t *testing.T) {
			page := testWindowPage()
			mutate(page)
			_, err := EncodePullPage(page)
			requirePullCode(t, err, types.WatchInvalidEvent)
			_, err = EncodeJSONPullPage(page)
			requirePullCode(t, err, types.WatchInvalidEvent)
		})
	}
	for name, mutate := range map[string]func(*pb.PullResponse){
		"explicit false bootstrap": func(p *pb.PullResponse) { p.BootstrapComplete = proto.Bool(false) },
		"event field":              func(p *pb.PullResponse) { p.Events = []*pb.ReplicationEvent{{Type: "delete", Id: "alice"}} },
		"missing complete":         func(p *pb.PullResponse) { p.Complete = nil },
		"false complete":           func(p *pb.PullResponse) { p.Complete = proto.Bool(false) },
		"nil order":                func(p *pb.PullResponse) { p.EffectiveOrder = []*pb.OrderBy{nil} },
		"nil document":             func(p *pb.PullResponse) { p.Documents = []*pb.Document{nil} },
		"duplicate metadata":       func(p *pb.PullResponse) { p.Documents[0].Id = "physical" },
		"non object":               func(p *pb.PullResponse) { p.Documents[0].Data = []byte(`{"type":"null"}`) },
		"invalid encoding":         func(p *pb.PullResponse) { p.Documents[0].Data = []byte(`{"type":"object","value":false}`) },
		"duplicate typed fields":   func(p *pb.PullResponse) { p.Documents[0].Data = []byte(`{"type":"object","value":{},"value":{}}`) },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := EncodePullPage(testWindowPage())
			require.NoError(t, err)
			mutate(encoded)
			page, err := DecodePullPage(encoded)
			requirePullCode(t, err, types.WatchInvalidEvent)
			require.Nil(t, page)
		})
	}
	for _, mode := range []string{"legacy", "events"} {
		for _, field := range []string{"complete", "requestId", "effectiveOrder"} {
			t.Run(mode+field, func(t *testing.T) {
				page := &types.ReplicationPullResponse{Checkpoint: "cp"}
				if mode == "events" {
					page.ProtocolVersion = 1
					page.Mode = "events"
					page.DatabaseIdentity = "entity"
					page.SourceHash = "hash"
					page.GenerationID = "gen"
					page.Phase = "scan"
				}
				encoded, err := EncodePullPage(page)
				require.NoError(t, err)
				switch field {
				case "complete":
					page.Complete = proto.Bool(false)
					encoded.Complete = page.Complete
				case "requestId":
					page.RequestID = proto.String("")
					encoded.RequestId = page.RequestID
				case "effectiveOrder":
					page.EffectiveOrder = []model.Order{{Field: "id", Direction: "asc"}}
					encoded.EffectiveOrder = []*pb.OrderBy{{Field: "id", Direction: "asc"}}
				}
				_, err = EncodePullPage(page)
				requirePullCode(t, err, types.WatchInvalidEvent)
				_, err = DecodePullPage(encoded)
				requirePullCode(t, err, types.WatchInvalidEvent)
			})
		}
	}
}

func TestWindowExactEnvelopeBudgets(t *testing.T) {
	page := testWindowPage()
	initial, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	page.Documents[0]["payload"] = strings.Repeat("x", MaxPageBytes-len(initial))
	raw, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	require.Len(t, raw, MaxPageBytes)
	encoded, err := EncodePullPage(page)
	require.NoError(t, err)
	_, err = DecodePullPage(encoded)
	require.NoError(t, err)
	*page.RequestID += "x"
	_, err = EncodeJSONPullPage(page)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	_, err = EncodePullPage(page)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	page = testWindowPage()
	overhead := proto.Size(windowProtoEnvelope(page))
	require.NoError(t, CheckPullPageSize(page, 0, MaxGRPCBytes-overhead))
	require.ErrorIs(t, CheckPullPageSize(page, 0, MaxGRPCBytes-overhead+1), model.ErrQueryWorkLimit)
	requirePullCode(t, CheckPullPageSize(page, -1, 0), types.WatchInvalidEvent)
	page.Documents = make([]model.Document, MaxPullLimit+1)
	_, err = EncodePullPage(page)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	encoded = windowProtoEnvelope(testWindowPage())
	encoded.Documents = make([]*pb.Document, MaxPullLimit+1)
	_, err = DecodePullPage(encoded)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
}

func TestWindowRequestPreservesForbiddenPresence(t *testing.T) {
	for _, field := range []string{"checkpoint", "limit"} {
		request := types.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "entity", Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}, Limit: windowLimit(3)}, RequestID: proto.String("request")}
		if field == "checkpoint" {
			request.CheckpointPresent = true
		} else {
			request.LimitPresent = true
		}
		encoded, err := EncodePullRequest("db", request)
		require.NoError(t, err)
		data, err := proto.Marshal(encoded)
		require.NoError(t, err)
		var received pb.PullRequest
		require.NoError(t, proto.Unmarshal(data, &received))
		decoded, err := DecodePullRequest(&received)
		require.NoError(t, err)
		require.Equal(t, request, decoded)
	}
	// Presence has no distinct semantic meaning in the existing collection mode.
	decoded, err := DecodePullRequest(&pb.PullRequest{WireVersion: Version, Checkpoint: proto.String(""), Limit: proto.Int32(0)})
	require.NoError(t, err)
	require.False(t, decoded.CheckpointPresent)
	require.False(t, decoded.LimitPresent)
}

func TestWindowErrorsRemainTypedAcrossRPC(t *testing.T) {
	for _, test := range []struct {
		err  error
		code codes.Code
	}{
		{types.ErrReplicationWindowIncomplete, codes.Unavailable},
		{indexer.ErrNoMatchingIndex, codes.FailedPrecondition},
		{indexer.ErrIndexNotReady, codes.Unavailable},
		{indexer.ErrIndexRebuilding, codes.Unavailable},
		{model.ErrQueryWorkLimit, codes.ResourceExhausted},
		{context.Canceled, codes.Canceled},
	} {
		t.Run(test.err.Error(), func(t *testing.T) {
			encoded := ReplicationErrorToStatus(fmt.Errorf("private detail: %w", test.err))
			require.Equal(t, test.code, status.Code(encoded))
			require.NotContains(t, encoded.Error(), "private detail")
			require.ErrorIs(t, ReplicationStatusToError(encoded), test.err)
		})
	}
}

func windowLimit(n int) *int { return &n }

func TestWindowResponseScopeRejectsDifferentRequests(t *testing.T) {
	request := types.ReplicationPullRequest{Collection: "users", DatabaseIdentity: "entity", Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{}, Limit: windowLimit(1)}, RequestID: proto.String("request")}
	require.NoError(t, ValidatePullResponseScope(request, testWindowPage()))
	for name, mutate := range map[string]func(*types.ReplicationPullResponse){
		"request mismatch":    func(p *types.ReplicationPullResponse) { p.RequestID = proto.String("older") },
		"collection mismatch": func(p *types.ReplicationPullResponse) { p.Documents[0]["collection"] = "other" },
		"database mismatch":   func(p *types.ReplicationPullResponse) { p.DatabaseIdentity = "reassigned" },
		"too many documents": func(p *types.ReplicationPullResponse) {
			p.Documents = append(p.Documents, model.Document{"id": "bob", "collection": "users"})
		},
		"incomplete":      func(p *types.ReplicationPullResponse) { p.Complete = proto.Bool(false) },
		"event downgrade": func(p *types.ReplicationPullResponse) { p.Mode = "events" },
	} {
		t.Run(name, func(t *testing.T) {
			page := testWindowPage()
			mutate(page)
			requirePullCode(t, ValidatePullResponseScope(request, page), types.WatchInvalidEvent)
		})
	}
	request.RequestID = nil
	requirePullCode(t, ValidatePullResponseScope(request, testWindowPage()), types.WatchInvalidEvent)
}
