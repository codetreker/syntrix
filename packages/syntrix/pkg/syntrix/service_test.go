package syntrix

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewService(t *testing.T) {
	svc := NewService()
	assert.NotNil(t, svc)
}
