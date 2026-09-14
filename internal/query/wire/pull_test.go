package wire

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func requirePullCode(t *testing.T, err error, code types.ReplicationErrorCode) {
	t.Helper()
	var failure *types.ReplicationError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, code, failure.Code)
}

func TestPullPageTransportRoundTrip(t *testing.T) {
	page := &types.ReplicationPullResponse{
		Documents: []model.Document{
			{"id": "alice", "collection": "users", "version": int64(math.MaxInt64), "nested": []any{int64(math.MinInt64), map[string]any{"type": "int64", "value": "business"}}},
			{"id": "bob", "collection": "users", "deleted": true},
		},
		Checkpoint: "opaque", CaughtUp: true,
	}
	encoded, err := EncodePullPage(page)
	require.NoError(t, err)
	raw, err := proto.Marshal(encoded)
	require.NoError(t, err)
	var received pb.PullResponse
	require.NoError(t, proto.Unmarshal(raw, &received))
	decoded, err := DecodePullPage(&received)
	require.NoError(t, err)
	assert.Equal(t, page, decoded)
	jsonBytes, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	var envelope jsonPullEnvelope
	require.NoError(t, json.Unmarshal(jsonBytes, &envelope))
	assert.Equal(t, page.Checkpoint, envelope.Checkpoint)
	assert.Equal(t, page.CaughtUp, envelope.CaughtUp)
	for i, document := range envelope.Documents {
		value, err := model.DecodeTypedValue(document)
		require.NoError(t, err)
		assert.Equal(t, map[string]any(page.Documents[i]), value)
	}
	assert.Len(t, decoded.Documents[1], 3)
}

func TestPullPageEmptyProgress(t *testing.T) {
	page := &types.ReplicationPullResponse{Checkpoint: "advanced", CaughtUp: false}
	raw, err := EncodeJSONPullPage(page)
	require.NoError(t, err)
	assert.JSONEq(t, `{"documents":[],"checkpoint":"advanced","caughtUp":false}`, string(raw))
	encoded, err := EncodePullPage(page)
	require.NoError(t, err)
	decoded, err := DecodePullPage(encoded)
	require.NoError(t, err)
	assert.NotNil(t, decoded.Documents)
	assert.False(t, decoded.CaughtUp)
}

func TestPullPageRejectsMalformedResponses(t *testing.T) {
	object, err := model.EncodeTypedValue(map[string]any{"id": "alice", "collection": "users"})
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		response *pb.PullResponse
	}{
		{"nil", nil},
		{"old version", &pb.PullResponse{Checkpoint: "cp"}},
		{"future version", &pb.PullResponse{WireVersion: Version + 1, Checkpoint: "cp"}},
		{"missing checkpoint", &pb.PullResponse{WireVersion: Version}},
		{"nil document", &pb.PullResponse{WireVersion: Version, Checkpoint: "cp", Documents: []*pb.Document{nil}}},
		{"raw JSON", &pb.PullResponse{WireVersion: Version, Checkpoint: "cp", Documents: []*pb.Document{{Data: []byte(`{"id":"alice"}`)}}}},
		{"scalar", &pb.PullResponse{WireVersion: Version, Checkpoint: "cp", Documents: []*pb.Document{{Data: []byte(`{"type":"int64","value":"3"}`)}}}},
		{"duplicate metadata", &pb.PullResponse{WireVersion: Version, Checkpoint: "cp", Documents: []*pb.Document{{Id: "physical", Data: object}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := DecodePullPage(tc.response)
			requirePullCode(t, err, types.ReplicationInvalidState)
			assert.Nil(t, page)
		})
	}
	for _, doc := range []model.Document{nil, {}, {"id": "alice"}, {"id": "a/b", "collection": "users"}, {"id": "alice", "collection": "users", "deleted": "true"}, {"id": "alice", "collection": "users", "value": math.NaN()}} {
		_, err := EncodePullPage(&types.ReplicationPullResponse{Checkpoint: "cp", Documents: []model.Document{doc}})
		requirePullCode(t, err, types.ReplicationInvalidState)
	}
}

func TestPullPageExactEnvelopeBudget(t *testing.T) {
	page := &types.ReplicationPullResponse{Checkpoint: "cp", Documents: []model.Document{{"id": "alice", "collection": "users", "payload": ""}}}
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
	documentBytes, protobufBytes := 0, 0
	for _, doc := range encoded.Documents {
		documentBytes += len(doc.Data)
		protobufBytes += proto.Size(&pb.PullResponse{Documents: []*pb.Document{doc}})
	}
	require.NoError(t, CheckPullPageSize(page, documentBytes, protobufBytes))
	page.Checkpoint += "x"
	_, err = EncodeJSONPullPage(page)
	requirePullCode(t, err, types.ReplicationBudgetExceeded)
	_, err = EncodePullPage(page)
	requirePullCode(t, err, types.ReplicationBudgetExceeded)
	requirePullCode(t, CheckPullPageSize(page, documentBytes, protobufBytes), types.ReplicationBudgetExceeded)
	encoded.Checkpoint = page.Checkpoint
	_, err = DecodePullPage(encoded)
	requirePullCode(t, err, types.ReplicationBudgetExceeded)
}

func TestPullPageBudgetRejectsOversizedInput(t *testing.T) {
	for _, response := range []*types.ReplicationPullResponse{
		{Checkpoint: strings.Repeat("c", MaxPullCursorBytes+1)},
		{Checkpoint: "cp", Documents: make([]model.Document, types.MaxReplicationLimit+1)},
	} {
		_, err := EncodePullPage(response)
		requirePullCode(t, err, types.ReplicationBudgetExceeded)
	}
	_, err := DecodePullPage(&pb.PullResponse{WireVersion: Version, Checkpoint: "cp", Documents: []*pb.Document{{Data: make([]byte, MaxGRPCBytes)}}})
	requirePullCode(t, err, types.ReplicationBudgetExceeded)
	page := &types.ReplicationPullResponse{Checkpoint: strings.Repeat("c", MaxPullCursorBytes)}
	_, err = EncodePullPage(page)
	require.NoError(t, err)
	_, err = DecodePullPage(&pb.PullResponse{WireVersion: Version, Checkpoint: page.Checkpoint + "c"})
	requirePullCode(t, err, types.ReplicationBudgetExceeded)
	requirePullCode(t, CheckPullPageSize(page, 0, MaxGRPCBytes), types.ReplicationBudgetExceeded)
}

func TestReplicationErrorTransportRoundTrip(t *testing.T) {
	for _, code := range []types.ReplicationErrorCode{
		types.ReplicationInvalidCursor, types.ReplicationScopeMismatch, types.ReplicationSourceMismatch,
		types.ReplicationHistoryUnavailable, types.ReplicationIdentityUnavailable, types.ReplicationUnsupported,
		types.ReplicationUnavailable, types.ReplicationPermissionDenied, types.ReplicationBudgetExceeded, types.ReplicationInvalidState,
	} {
		t.Run(string(code), func(t *testing.T) {
			raw := &types.ReplicationError{Code: code, Database: "private-database", Collection: "private-collection", Cause: errors.New("secret source token")}
			transport := ReplicationErrorToStatus(raw)
			assert.NotContains(t, transport.Error(), "secret")
			assert.NotContains(t, transport.Error(), "private")
			requirePullCode(t, ReplicationStatusToError(transport), code)
		})
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		err := ReplicationErrorToStatus(&types.ReplicationError{Code: types.ReplicationUnavailable, Cause: cause})
		require.ErrorIs(t, ReplicationStatusToError(err), cause)
	}
	assert.Nil(t, ReplicationErrorToStatus(nil))
	assert.Nil(t, ReplicationStatusToError(nil))
	unknown := ReplicationErrorToStatus(errors.New("secret"))
	assert.Equal(t, codes.Internal, status.Code(unknown))
	assert.NotContains(t, unknown.Error(), "secret")
	requirePullCode(t, ReplicationStatusToError(status.Error(codes.Unavailable, "secret")), types.ReplicationUnavailable)
	for _, detail := range []*errdetails.ErrorInfo{
		{Domain: "foreign", Reason: string(types.ReplicationHistoryUnavailable)},
		{Domain: "syntrix.replication", Reason: "FUTURE"},
		{Domain: "syntrix.replication", Reason: string(types.ReplicationInvalidCursor)},
	} {
		st, err := status.New(codes.FailedPrecondition, "secret").WithDetails(detail)
		require.NoError(t, err)
		decoded := ReplicationStatusToError(st.Err())
		requirePullCode(t, decoded, types.ReplicationInvalidState)
		assert.NotContains(t, decoded.Error(), "secret")
	}
}
