package cel

import (
	"errors"
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/assert"
)

func TestNewEvaluator_Coverage(t *testing.T) {
	// Save original celNewEnv and restore after test
	originalCelNewEnv := celNewEnv
	defer func() { celNewEnv = originalCelNewEnv }()

	// Case 1: Success
	eval, err := NewEvaluator()
	assert.NoError(t, err)
	assert.NotNil(t, eval)

	// Case 2: Error
	celNewEnv = func(opts ...cel.EnvOption) (*cel.Env, error) {
		return nil, errors.New("mock cel error")
	}
	eval, err = NewEvaluator()
	assert.Error(t, err)
	assert.Nil(t, eval)
	assert.Equal(t, "mock cel error", err.Error())
}
