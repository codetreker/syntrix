package rest

import (
	"net/http"

	"github.com/codetreker/syntrix/internal/ctxkeys"
	"github.com/codetreker/syntrix/internal/gateway/replication"
)

func replicationPrincipal(r *http.Request) replication.Principal {
	subject, _ := r.Context().Value(ctxkeys.KeyUserID).(string)
	grants, _ := r.Context().Value(ctxkeys.KeyDBAdmin).([]string)
	return replication.Principal{Subject: subject, DBAdmin: grants}
}

func writeReplicationFailure(w http.ResponseWriter, failure replication.Failure) {
	if failure.Status == 499 {
		w.WriteHeader(499)
		return
	}
	writeError(w, failure.Status, failure.Code, failure.Message)
}
