package rest

import (
	"bytes"
	"encoding/json"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReplicaChangeCreateConditionPresence(t *testing.T) {
	var reused ReplicaChange
	for _, condition := range []storage.CreateCondition{storage.CreateIfTombstone, storage.CreateIfAbsent, ""} {
		doc := model.Document{"id": "alice"}
		if condition == storage.CreateIfTombstone {
			doc["version"] = int64(0)
		}
		encoded, err := json.Marshal(ReplicaChange{Action: "create", CreateCondition: condition, Doc: doc})
		require.NoError(t, err)
		require.Equal(t, condition != "", bytes.Contains(encoded, []byte(`"createCondition"`)))
		require.NoError(t, json.Unmarshal(encoded, &reused))
		require.Equal(t, condition, reused.CreateCondition)
		require.Equal(t, doc, reused.Doc)
	}
	require.Nil(t, reused.BaseVersion)
}

func TestHandlePushCreateConditions(t *testing.T) {
	for _, condition := range []storage.CreateCondition{"", storage.CreateIfAbsent, storage.CreateIfTombstone} {
		service := new(MockQueryService)
		service.On("Push", mock.Anything, "default", mock.MatchedBy(func(req storage.ReplicationPushRequest) bool {
			return len(req.Changes) == 1 && req.Changes[0].CreateCondition == condition && (condition != storage.CreateIfTombstone || (req.Changes[0].BaseVersion != nil && *req.Changes[0].BaseVersion == 0))
		})).Return(&storage.ReplicationPushResponse{}, nil).Once()
		doc := model.Document{"id": "alice"}
		if condition == storage.CreateIfTombstone {
			doc["version"] = int64(0)
		}
		body := typedPushBody(t, "users", ReplicaChange{Action: "create", Doc: doc, CreateCondition: condition})
		response := httptest.NewRecorder()
		createTestServer(service, nil, nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body)))
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		service.AssertExpectations(t)
	}
}

func TestHandlePushRejectsInvalidCreateConditionBeforeService(t *testing.T) {
	for _, test := range []struct {
		action, condition string
		version           bool
	}{
		{"create", "null", false}, {"create", `""`, false}, {"create", `"unknown"`, false}, {"create", "0", false}, {"create", "true", false}, {"create", "{}", false}, {"create", "[]", false},
		{"update", `"absent"`, false}, {"delete", `"tombstone"`, true}, {"create", `"absent"`, true}, {"create", `"tombstone"`, false},
	} {
		service := new(MockQueryService)
		first, err := json.Marshal(ReplicaChange{Action: "create", Doc: model.Document{"id": "first"}})
		require.NoError(t, err)
		doc := model.Document{"id": "bad"}
		if test.version {
			doc["version"] = int64(0)
		}
		document, err := model.EncodeTypedValue(doc)
		require.NoError(t, err)
		body := `{"collection":"users","changes":[` + string(first) + `,{"action":"` + test.action + `","createCondition":` + test.condition + `,"document":` + string(document) + `}]}`
		response := httptest.NewRecorder()
		createTestServer(service, nil, nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/replication/v1/databases/default/push", bytes.NewBufferString(body)))
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		service.AssertNotCalled(t, "Push", mock.Anything, mock.Anything, mock.Anything)
	}
}
