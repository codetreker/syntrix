package types

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFatalError(t *testing.T) {
	baseErr := errors.New("base error")
	fatalErr := &FatalError{Err: baseErr}

	assert.Equal(t, "fatal error: base error", fatalErr.Error())
	assert.Equal(t, baseErr, fatalErr.Unwrap())
}

func TestIsFatal(t *testing.T) {
	baseErr := errors.New("base error")
	fatalErr := &FatalError{Err: baseErr}
	wrappedErr := fmt.Errorf("wrapped: %w", fatalErr)

	assert.True(t, IsFatal(fatalErr))
	assert.True(t, IsFatal(wrappedErr))
	assert.False(t, IsFatal(baseErr))
	assert.False(t, IsFatal(nil))
}

func TestRetryAfterError(t *testing.T) {
	baseErr := errors.New("webhook failed with status: 429")
	hint := &RetryAfterError{Err: baseErr, Delay: time.Minute}
	wrapped := fmt.Errorf("delivery failed: %w", hint)

	assert.Equal(t, baseErr.Error(), hint.Error())
	assert.Same(t, baseErr, hint.Unwrap())
	assert.ErrorIs(t, wrapped, baseErr)
	var extracted *RetryAfterError
	require.ErrorAs(t, wrapped, &extracted)
	assert.Same(t, hint, extracted)
	assert.Equal(t, time.Minute, extracted.Delay)
	assert.False(t, IsFatal(wrapped))
}
