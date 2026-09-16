package types

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWatchErrorPreservesReasonScopeAndCause(t *testing.T) {
	failure := &WatchError{
		Code: WatchSourceUnavailable, Database: "database", Collection: "users",
		Cause: fmt.Errorf("read interrupted: %w", context.DeadlineExceeded),
	}
	err := fmt.Errorf("consumer: %w", failure)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var watchErr *WatchError
	require.ErrorAs(t, err, &watchErr)
	require.Same(t, failure, watchErr)
	require.Equal(t, WatchSourceUnavailable, watchErr.Code)
	require.Contains(t, err.Error(), `database "database" collection "users"`)
	require.NotContains(t, err.Error(), "read interrupted")
	require.Contains(t, watchErr.Cause.Error(), "read interrupted")
	require.False(t, errors.Is(err, context.Canceled))
}

func TestWatchOptionsResolve(t *testing.T) {
	for _, tc := range []struct {
		name  string
		after WatchCheckpoint
		opts  WatchOptions
		await time.Duration
	}{
		{name: "current default", await: time.Second},
		{name: "resume default", after: "opaque", await: time.Second},
		{name: "scan boundary", opts: WatchOptions{StartMode: WatchStartForScan, IncludeBefore: true}, await: time.Second},
		{name: "minimum poll", opts: WatchOptions{MaxAwaitTime: time.Millisecond}, await: time.Millisecond},
		{name: "long poll", opts: WatchOptions{MaxAwaitTime: time.Hour}, await: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := tc.opts.Resolve(tc.after)
			require.NoError(t, err)
			require.Equal(t, tc.await, resolved.MaxAwaitTime)
			require.Equal(t, tc.opts.StartMode, resolved.StartMode)
			require.Equal(t, tc.opts.IncludeBefore, resolved.IncludeBefore)
		})
	}
	for _, tc := range []struct {
		name  string
		after WatchCheckpoint
		opts  WatchOptions
	}{
		{name: "unknown mode", opts: WatchOptions{StartMode: 2}},
		{name: "negative mode", opts: WatchOptions{StartMode: -1}},
		{name: "scan resume", after: "sensitive-checkpoint", opts: WatchOptions{StartMode: WatchStartForScan}},
		{name: "negative await", opts: WatchOptions{MaxAwaitTime: -time.Second}},
		{name: "submillisecond await", opts: WatchOptions{MaxAwaitTime: time.Microsecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := tc.opts.Resolve(tc.after)
			require.Error(t, err)
			require.Zero(t, resolved)
			require.NotContains(t, err.Error(), "sensitive-checkpoint")
		})
	}
}
