package wire

import (
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
	"math"
	"strings"
	"testing"
)

func pushFixture() types.ReplicationPushRequest {
	return types.ReplicationPushRequest{Collection: "users", Changes: []types.ReplicationPushChange{{Action: types.PushUpdate, Doc: &types.StoredDoc{Database: "db", Collection: "users", Fullpath: "users/alice", Id: "hash", Version: 1, Data: map[string]interface{}{"name": "Alice", "nested": map[string]interface{}{"large": int64(math.MaxInt64), "integralFloat": float64(1), "fraction": 1.5}}}}}}
}

func TestPushRequestRoundTrip(t *testing.T) {
	for _, version := range []*int64{nil, proto.Int64(0), proto.Int64(1), proto.Int64(9007199254740993), proto.Int64(math.MaxInt64)} {
		for _, action := range []types.PushAction{types.PushCreate, types.PushUpdate, types.PushDelete} {
			req := pushFixture()
			req.Changes[0].BaseVersion = version
			req.Changes[0].Action = action
			encoded, err := EncodePushRequest("db", req)
			require.NoError(t, err)
			payload, err := proto.Marshal(encoded)
			require.NoError(t, err)
			var remote pb.PushRequest
			require.NoError(t, proto.Unmarshal(payload, &remote))
			decoded, err := DecodePushRequest(&remote)
			require.NoError(t, err)
			require.Equal(t, req, decoded)
		}
	}
}

func TestPushRequestRejectsMalformedBatch(t *testing.T) {
	for _, mutate := range []func(*pb.PushRequest){
		func(r *pb.PushRequest) { r.Changes = append(r.Changes, nil) },
		func(r *pb.PushRequest) { r.Changes[0].Document = nil },
		func(r *pb.PushRequest) { r.Changes[0].Action = 0 },
		func(r *pb.PushRequest) { r.Changes[0].Action = 99 },
		func(r *pb.PushRequest) { r.Changes[0].BaseVersion = proto.Int64(-1) },
		func(r *pb.PushRequest) { r.Changes[0].Document.Data = []byte("{") },
		func(r *pb.PushRequest) { r.Changes[0].Document.Data = []byte("[]") },
		func(r *pb.PushRequest) { r.Changes[0].Document.Data = []byte(`{"type":"array","value":[]}`) },
		func(r *pb.PushRequest) { r.Changes[0].Document.Data = []byte(`{"name":"plain legacy"}`) },
		func(r *pb.PushRequest) { r.Changes[0].Document.Data = []byte(`{"name":"\ud800"}`) },
		func(r *pb.PushRequest) { r.Changes[0].Document.Data = []byte{123, 34, 110, 34, 58, 34, 255, 34, 125} },
	} {
		encoded, err := EncodePushRequest("db", pushFixture())
		require.NoError(t, err)
		mutate(encoded)
		_, err = DecodePushRequest(encoded)
		require.ErrorIs(t, err, model.ErrInvalidQuery)
	}
	_, err := DecodePushRequest(nil)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	for _, mutate := range []func(*types.ReplicationPushRequest){
		func(r *types.ReplicationPushRequest) { r.Changes[0].Action = "" },
		func(r *types.ReplicationPushRequest) { r.Changes[0].Doc = nil },
		func(r *types.ReplicationPushRequest) { r.Changes[0].BaseVersion = proto.Int64(-1) },
		func(r *types.ReplicationPushRequest) {
			r.Changes[0].Doc.Data = map[string]interface{}{"bad": make(chan int)}
		},
	} {
		req := pushFixture()
		mutate(&req)
		_, err := EncodePushRequest("db", req)
		require.ErrorIs(t, err, model.ErrInvalidQuery)
	}
}

func TestPushConflictRoundTrip(t *testing.T) {
	req := pushFixture()
	req.Changes[0].BaseVersion = proto.Int64(5)
	for _, reason := range []types.PushConflictReason{types.PushMissing, types.PushTombstoned, types.PushVersionMismatch, types.PushPreconditionFailed, types.PushAlreadyExists} {
		current := *req.Changes[0].Doc
		current.Version = 5
		response := &types.ReplicationPushResponse{Conflicts: []types.ReplicationPushConflict{{ID: "alice", Reason: reason, Current: &current}}}
		change := req.Changes[0]
		req.Changes[0].Action = types.PushUpdate
		switch reason {
		case types.PushMissing:
			response.Conflicts[0].Current = nil
		case types.PushTombstoned:
			current.Deleted = true
			current.Data = map[string]interface{}{}
		case types.PushVersionMismatch:
			current.Version = math.MaxInt64
		case types.PushAlreadyExists:
			req.Changes[0].Action = types.PushCreate
		}
		encoded, err := EncodePushResponse("db", req, response)
		require.NoError(t, err)
		payload, err := proto.Marshal(encoded)
		require.NoError(t, err)
		var remote pb.PushResponse
		require.NoError(t, proto.Unmarshal(payload, &remote))
		decoded, err := DecodePushResponse("db", req, &remote)
		require.NoError(t, err)
		require.Equal(t, response, decoded)
		req.Changes[0] = change
	}
	req.Changes = append(req.Changes, req.Changes[0])
	req.Changes[1].Doc = &types.StoredDoc{Data: map[string]interface{}{"id": "alice"}}
	response := &types.ReplicationPushResponse{Conflicts: []types.ReplicationPushConflict{{ID: "alice", Reason: types.PushMissing}, {ChangeIndex: 1, ID: "alice", Reason: types.PushMissing}}}
	encoded, err := EncodePushResponse("db", req, response)
	require.NoError(t, err)
	decoded, err := DecodePushResponse("db", req, encoded)
	require.NoError(t, err)
	require.Equal(t, response, decoded)
}

func TestPushConflictRejectsInvalidResponse(t *testing.T) {
	req := pushFixture()
	for _, conflict := range []*pb.PushConflict{
		nil, {}, {ChangeIndex: -1, Id: "alice", Reason: "missing"}, {ChangeIndex: 1, Id: "alice", Reason: "missing"},
		{Id: "bob", Reason: "missing"}, {Id: "alice", Reason: "future"},
		{Id: "alice", Reason: "tombstoned"}, {Id: "alice", Reason: "version_mismatch"},
		{Id: "alice", Reason: "missing", Current: &pb.Document{Database: "db", Collection: "users", Fullpath: "users/alice", Data: []byte(`{"type":"null"}`)}},
		{Id: "alice", Reason: "tombstoned", Current: &pb.Document{Database: "other", Collection: "users", Fullpath: "users/alice", Deleted: true, Data: []byte(`{"type":"null"}`)}},
		{Id: "alice", Reason: "tombstoned", Current: &pb.Document{Database: "db", Collection: "users", Fullpath: "users/alice", Data: []byte("{")}},
	} {
		_, err := DecodePushResponse("db", req, &pb.PushResponse{Conflicts: []*pb.PushConflict{conflict}})
		require.Error(t, err)
	}
	_, err := DecodePushResponse("db", req, nil)
	require.Error(t, err)
	_, err = EncodePushResponse("db", req, nil)
	require.Error(t, err)
	c := &pb.PushConflict{Id: "alice", Reason: "missing"}
	_, err = DecodePushResponse("db", req, &pb.PushResponse{Conflicts: []*pb.PushConflict{c, c}})
	require.Error(t, err)
	current := *req.Changes[0].Doc
	current.Deleted = true
	current.Data = map[string]interface{}{"bad": make(chan int)}
	response := &types.ReplicationPushResponse{Conflicts: []types.ReplicationPushConflict{{ID: "alice", Reason: types.PushTombstoned, Current: &current}}}
	_, err = EncodePushResponse("db", req, response)
	require.Error(t, err)
	req.Changes[0].Doc = nil
	_, err = DecodePushResponse("db", req, &pb.PushResponse{Conflicts: []*pb.PushConflict{c}})
	require.Error(t, err)
}

func TestPushRejectsLegacyDocumentConflicts(t *testing.T) {
	legacy := &pb.GetDocumentResponse{Document: &pb.Document{Id: "hash", Fullpath: "users/alice", Data: []byte(`{"name":"Alice"}`)}}
	payload, err := proto.Marshal(legacy)
	require.NoError(t, err)
	var response pb.PushResponse
	err = proto.Unmarshal(payload, &response)
	if err == nil {
		_, err = DecodePushResponse("db", pushFixture(), &response)
	}
	require.Error(t, err)
}

func TestPushEncodedByteBoundaries(t *testing.T) {
	req := pushFixture()
	req.Changes[0].Action = types.PushCreate
	req.Changes[0].CreateCondition = types.CreateIfAbsent
	req.Changes[0].Doc.Data = map[string]interface{}{"padding": strings.Repeat("x", MaxGRPCBytes-1024)}
	encoded, err := EncodePushRequest("db", req)
	require.NoError(t, err)
	padding := req.Changes[0].Doc.Data["padding"].(string)
	req.Changes[0].Doc.Data["padding"] = padding + strings.Repeat("x", MaxGRPCBytes-proto.Size(encoded))
	encoded, err = EncodePushRequest("db", req)
	require.NoError(t, err)
	require.Equal(t, MaxGRPCBytes, proto.Size(encoded))
	_, err = DecodePushRequest(encoded)
	require.NoError(t, err)
	req.Changes[0].Doc.Data["padding"] = req.Changes[0].Doc.Data["padding"].(string) + "x"
	_, err = EncodePushRequest("db", req)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	encoded.Changes[0].Document.Data = append(encoded.Changes[0].Document.Data, ' ')
	_, err = DecodePushRequest(encoded)
	require.ErrorIs(t, err, model.ErrInvalidQuery)

	req = pushFixture()
	req.Changes[0].Action = types.PushCreate
	current := *req.Changes[0].Doc
	current.Data = map[string]interface{}{"padding": strings.Repeat("x", MaxGRPCBytes-1024)}
	response := &types.ReplicationPushResponse{Conflicts: []types.ReplicationPushConflict{{ID: "alice", Reason: types.PushAlreadyExists, Current: &current}}}
	out, err := EncodePushResponse("db", req, response)
	require.NoError(t, err)
	current.Data["padding"] = current.Data["padding"].(string) + strings.Repeat("x", MaxGRPCBytes-proto.Size(out))
	out, err = EncodePushResponse("db", req, response)
	require.NoError(t, err)
	require.Equal(t, MaxGRPCBytes, proto.Size(out))
	_, err = DecodePushResponse("db", req, out)
	require.NoError(t, err)
	current.Data["padding"] = current.Data["padding"].(string) + "x"
	_, err = EncodePushResponse("db", req, response)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	out.Conflicts[0].Current.Data = append(out.Conflicts[0].Current.Data, ' ')
	_, err = DecodePushResponse("db", req, out)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
}

func TestPushResponseBudgetIncludesAllConflicts(t *testing.T) {
	req := pushFixture()
	req.Changes[0].Action = types.PushCreate
	req.Changes = append(req.Changes, req.Changes[0])
	current := *req.Changes[0].Doc
	current.Data = map[string]interface{}{"large": strings.Repeat("x", MaxGRPCBytes/2)}
	response := &types.ReplicationPushResponse{Conflicts: []types.ReplicationPushConflict{{ID: "alice", Reason: types.PushAlreadyExists, Current: &current}}}
	_, err := EncodePushResponse("db", req, response)
	require.NoError(t, err)
	response.Conflicts = append(response.Conflicts, types.ReplicationPushConflict{ChangeIndex: 1, ID: "alice", Reason: types.PushAlreadyExists, Current: &current})
	encoded, err := EncodePushResponse("db", req, response)
	require.Nil(t, encoded)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
}

func TestPushCreateConditionPresence(t *testing.T) {
	for _, condition := range []types.CreateCondition{"", types.CreateIfAbsent, types.CreateIfTombstone} {
		req := pushFixture()
		req.Changes[0].Action = types.PushCreate
		req.Changes[0].CreateCondition = condition
		if condition == types.CreateIfTombstone {
			req.Changes[0].BaseVersion = proto.Int64(0)
		}
		encoded, err := EncodePushRequest("db", req)
		require.NoError(t, err)
		require.Equal(t, condition == "", encoded.Changes[0].CreateCondition == nil)
		payload, err := proto.Marshal(encoded)
		require.NoError(t, err)
		var remote pb.PushRequest
		require.NoError(t, proto.Unmarshal(payload, &remote))
		decoded, err := DecodePushRequest(&remote)
		require.NoError(t, err)
		require.Equal(t, req, decoded)
	}
	for _, condition := range []pb.PushCreateCondition{pb.PushCreateCondition_PUSH_CREATE_CONDITION_UNSPECIFIED, pb.PushCreateCondition(99)} {
		encoded, err := EncodePushRequest("db", pushFixture())
		require.NoError(t, err)
		encoded.Changes[0].CreateCondition = condition.Enum()
		_, err = DecodePushRequest(encoded)
		require.ErrorIs(t, err, model.ErrInvalidQuery)
	}
	req := pushFixture()
	req.Changes[0].CreateCondition = "unknown"
	_, err := EncodePushRequest("db", req)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
}

func TestConditionalCreateConflictStates(t *testing.T) {
	for _, condition := range []types.CreateCondition{types.CreateIfAbsent, types.CreateIfTombstone} {
		req := pushFixture()
		req.Changes[0].Action = types.PushCreate
		req.Changes[0].CreateCondition = condition
		if condition == types.CreateIfTombstone {
			req.Changes[0].BaseVersion = proto.Int64(4)
		}
		for _, reason := range []types.PushConflictReason{types.PushAlreadyExists, types.PushTombstoned, types.PushMissing} {
			current := *req.Changes[0].Doc
			current.Version = 9
			conflict := types.ReplicationPushConflict{ID: "alice", Reason: reason, Current: &current}
			if reason == types.PushTombstoned {
				current.Deleted = true
			}
			if reason == types.PushMissing {
				conflict.Current = nil
			}
			response := &types.ReplicationPushResponse{Conflicts: []types.ReplicationPushConflict{conflict}}
			encoded, err := EncodePushResponse("db", req, response)
			require.NoError(t, err)
			decoded, err := DecodePushResponse("db", req, encoded)
			require.NoError(t, err)
			require.Equal(t, response, decoded)
		}
	}
}

func TestConditionalCreateRejectsLegacyConflictReasons(t *testing.T) {
	req := pushFixture()
	req.Changes[0].Action = types.PushCreate
	req.Changes[0].CreateCondition = types.CreateIfTombstone
	req.Changes[0].BaseVersion = proto.Int64(4)
	current := *req.Changes[0].Doc
	for _, reason := range []types.PushConflictReason{types.PushVersionMismatch, types.PushPreconditionFailed} {
		current.Version = 3
		if reason == types.PushPreconditionFailed {
			current.Version = 4
		}
		response := &types.ReplicationPushResponse{Conflicts: []types.ReplicationPushConflict{{ID: "alice", Reason: reason, Current: &current}}}
		_, err := EncodePushResponse("db", req, response)
		require.Error(t, err)
	}
}
