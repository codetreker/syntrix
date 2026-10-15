package nats

import (
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNatsMessage_Data(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("hello world"))
	msg := WrapMessage(mockMsg)

	assert.Equal(t, []byte("hello world"), msg.Data())
}

func TestNatsMessage_Subject(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	msg := WrapMessage(mockMsg)

	assert.Equal(t, "test.subject", msg.Subject())
}

func TestNatsMessage_Ack(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Ack").Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.Ack()

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "Ack")
}

func TestNatsMessage_Nak(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Nak").Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.Nak()

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "Nak")
}

func TestNatsMessage_NakWithDelay(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("NakWithDelay", 5*time.Second).Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.NakWithDelay(5 * time.Second)

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "NakWithDelay", 5*time.Second)
}

func TestNatsMessage_Term(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Term").Return(nil)

	msg := WrapMessage(mockMsg)
	err := msg.Term()

	assert.NoError(t, err)
	mockMsg.AssertCalled(t, "Term")
}

func TestNatsMessage_Metadata(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMetadata := &jetstream.MsgMetadata{
		NumDelivered: 2,
		Timestamp:    time.Now(),
		Stream:       "STREAM",
		Consumer:     "CONSUMER",
	}
	mockMsg.On("Metadata").Return(mockMetadata, nil)

	msg := WrapMessage(mockMsg)
	md, err := msg.Metadata()

	require.NoError(t, err)
	assert.Equal(t, uint64(2), md.NumDelivered)
	assert.Equal(t, "STREAM", md.Stream)
	assert.Equal(t, "CONSUMER", md.Consumer)
	assert.Equal(t, "test.subject", md.Subject)
}

func TestNatsMessage_MetadataError(t *testing.T) {
	mockMsg := NewMockMsg("test.subject", []byte("data"))
	mockMsg.On("Metadata").Return(nil, assert.AnError)

	msg := WrapMessage(mockMsg)
	_, err := msg.Metadata()

	assert.Error(t, err)
}
