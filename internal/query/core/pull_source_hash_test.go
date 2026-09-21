package core

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestReplicationSourceHashReusesCanonicalQueryIdentity(t *testing.T) {
	request := types.ReplicationPullRequest{DatabaseIdentity: "0123456789abcdef", Collection: "users",
		Source: &types.ReplicationSource{Version: 1, Filters: model.Filters{
			{Field: "score", Op: model.OpEq, Value: int64(1)}, {Field: "active", Op: model.OpEq, Value: true},
		}}}
	hash, err := ReplicationSourceHash(request)
	require.NoError(t, err)
	require.Len(t, hash, 64)
	equivalent := request
	equivalent.Source = &types.ReplicationSource{Version: 1, Filters: model.Filters{
		{Field: "active", Op: model.OpEq, Value: true}, {Field: "score", Op: model.OpEq, Value: float64(1)},
	}, OrderBy: []model.Order{{Field: "id", Direction: "asc"}}}
	normalized, err := ReplicationSourceHash(equivalent)
	require.NoError(t, err)
	require.Equal(t, hash, normalized)
	changed := request
	changed.DatabaseIdentity = "fedcba9876543210"
	other, err := ReplicationSourceHash(changed)
	require.NoError(t, err)
	require.NotEqual(t, hash, other)
	limit, requestID := 10, "registration"
	window := equivalent
	window.Source = &types.ReplicationSource{Version: 1, Filters: request.Source.Filters, Limit: &limit}
	window.RequestID = &requestID
	windowHash, err := ReplicationSourceHash(window)
	require.NoError(t, err)
	require.NotEqual(t, hash, windowHash)
	requestID = "actual-read"
	readHash, err := ReplicationSourceHash(window)
	require.NoError(t, err)
	require.Equal(t, windowHash, readHash)
	_, err = ReplicationSourceHash(types.ReplicationPullRequest{})
	require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
	invalid := request
	invalid.Source = &types.ReplicationSource{Version: 2, Filters: model.Filters{}}
	_, err = ReplicationSourceHash(invalid)
	require.ErrorIs(t, err, types.ErrInvalidReplicationSource)
}
