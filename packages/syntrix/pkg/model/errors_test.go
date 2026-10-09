package model

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsCanceled(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"nil error", nil, false},
		{"context.Canceled", context.Canceled, true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"ErrCanceled", ErrCanceled, true},
		{"wrapped context.Canceled", fmt.Errorf("wrapped: %w", context.Canceled), true},
		{"wrapped ErrCanceled", fmt.Errorf("wrapped: %w", ErrCanceled), true},
		{"string contains context canceled", errors.New("operation failed: context canceled"), true},
		{"string contains context deadline exceeded", errors.New("timeout: context deadline exceeded"), true},
		{"unrelated error", errors.New("some other error"), false},
		{"ErrNotFound", ErrNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsCanceled(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestWrapError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected error
	}{
		{"nil error", nil, nil},
		{"context.Canceled", context.Canceled, ErrCanceled},
		{"context.DeadlineExceeded", context.DeadlineExceeded, ErrCanceled},
		{"ErrCanceled", ErrCanceled, ErrCanceled},
		{"string contains context canceled", errors.New("mongodb: context canceled"), ErrCanceled},
		{"unrelated error", ErrNotFound, ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := WrapError(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
