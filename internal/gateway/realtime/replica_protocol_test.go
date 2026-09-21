package realtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
)

func TestReplicaEnvelopeAndRequestValidation(t *testing.T) {
	for _, raw := range []string{
		`{"id":"a","id":"b","type":"auth","payload":{}}`,
		`{"id":"a","type":"auth","payload":{},"unknown":true}`,
		`{"id":"a","type":"unknown","payload":{}}`,
		`{"id":"\ud800","type":"auth","payload":{}}`,
		`{"id":"a","type":"auth"}`,
		`{"id":"a","type":"auth","payload":{}} {}`,
	} {
		_, err := decodeReplicaEnvelope([]byte(raw))
		require.Error(t, err, raw)
	}
	_, err := decodeReplicaEnvelope([]byte(strings.Repeat("x", replicaInputBytes+1)))
	require.Error(t, err)
	request := json.RawMessage(`{"collection":"users","source":{"version":1,"filters":[],"orderBy":[]},"checkpoint":null,"limit":100}`)
	frame, err := encodeReplicaFrame("read-1", TypeReplicaRead, replicaReadPayload{SubID: "sub-1", RequestID: "read-1", Request: request})
	require.NoError(t, err)
	message, err := decodeReplicaEnvelope(frame)
	require.NoError(t, err)
	payload, decoded, err := decodeReplicaRead(message, len(frame))
	require.NoError(t, err)
	require.Equal(t, "sub-1", payload.SubID)
	require.Equal(t, "users", decoded.Collection)
	require.Equal(t, 100, decoded.Limit)
	message.ID = "another-read"
	_, _, err = decodeReplicaRead(message, len(frame))
	require.Error(t, err)
	message.ID = "read-1"
	_, _, err = decodeReplicaRead(message, len(request)+replicaEnvelopeBytes+1)
	require.Error(t, err)
}

func TestReplicaReadPreservesPullErrorsAndCorrelation(t *testing.T) {
	cases := []struct{ name, request, code string }{
		{"numeric cursor", `{"collection":"users","source":{"version":1,"filters":[],"orderBy":[]},"checkpoint":12}`, "RESYNC_REQUIRED"},
		{"database injection", `{"collection":"users","source":{"version":1,"filters":[],"orderBy":[]},"expectedDatabaseIdentity":"0123456789abcdef"}`, "INVALID_REPLICATION_SOURCE"},
		{"window request mismatch", `{"collection":"users","source":{"version":1,"filters":[],"orderBy":[],"limit":1},"requestId":"old"}`, "REPLICATION_PROTOCOL_ERROR"},
		{"events request id", `{"collection":"users","source":{"version":1,"filters":[],"orderBy":[]},"requestId":"read-1"}`, "INVALID_REPLICATION_SOURCE"},
		{"no query source", `{"collection":"users"}`, "INVALID_REPLICATION_SOURCE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := encodeReplicaFrame("read-1", TypeReplicaRead, replicaReadPayload{SubID: "sub", RequestID: "read-1", Request: json.RawMessage(tc.request)})
			require.NoError(t, err)
			message, err := decodeReplicaEnvelope(frame)
			require.NoError(t, err)
			_, request, err := decodeReplicaRead(message, len(frame))
			require.Error(t, err)
			require.Equal(t, tc.code, replication.ClassifyPullError(request, err).Code)
		})
	}
	request, _ := json.Marshal(map[string]any{"collection": "users", "source": map[string]any{"version": 1, "filters": []any{}, "orderBy": []any{}}, "checkpoint": strings.Repeat("c", querycore.MaxPullCursorBytes+1)})
	frame, err := encodeReplicaFrame("r", TypeReplicaRead, replicaReadPayload{SubID: "s", RequestID: "r", Request: request})
	require.NoError(t, err)
	message, err := decodeReplicaEnvelope(frame)
	require.NoError(t, err)
	_, decoded, err := decodeReplicaRead(message, len(frame))
	require.Error(t, err)
	require.Equal(t, "REQUEST_TOO_LARGE", replication.ClassifyPullError(decoded, err).Code)
}

func TestReplicaSourceWindowAndStrictControls(t *testing.T) {
	var payload replicaSubscribePayload
	require.NoError(t, decodeReplicaObject([]byte(`{"collection":"users","source":{"version":1,"filters":[],"orderBy":[],"limit":10}}`), &payload))
	request, err := decodeReplicaSource(payload, "subscription")
	require.NoError(t, err)
	require.Equal(t, "subscription", *request.RequestID)
	require.Error(t, decodeReplicaObject([]byte(`{"subId":"one","subId":"two","requestId":"read"}`), new(replicaAckPayload)))
	require.Error(t, decodeReplicaObject([]byte(`{"subId":"one","requestId":"read","ignored":true}`), new(replicaAckPayload)))
	require.False(t, replicaText("a\x00b", replicaIDBytes))
	require.False(t, replicaText(strings.Repeat("a", replicaIDBytes+1), replicaIDBytes))
}
