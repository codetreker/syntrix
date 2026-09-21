package rest

import (
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
)

func replicationPrincipal(r *http.Request) replication.Principal {
	subject, _ := r.Context().Value(identity.ContextKeyUserID).(string)
	grants, _ := r.Context().Value(identity.ContextKeyDBAdmin).([]string)
	return replication.Principal{Subject: subject, DBAdmin: grants}
}

func writeReplicationFailure(w http.ResponseWriter, failure replication.Failure) {
	if failure.Status == 499 {
		w.WriteHeader(499)
		return
	}
	writeError(w, failure.Status, failure.Code, failure.Message)
}
