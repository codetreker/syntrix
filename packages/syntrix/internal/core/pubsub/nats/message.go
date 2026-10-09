package nats

import (
	"time"

	"github.com/codetreker/syntrix/internal/core/pubsub"
	"github.com/nats-io/nats.go/jetstream"
)

// natsMessage wraps a jetstream.Msg to implement pubsub.Message.
type natsMessage struct {
	msg jetstream.Msg
}

// WrapMessage wraps a jetstream.Msg as a pubsub.Message.
func WrapMessage(msg jetstream.Msg) pubsub.Message {
	return &natsMessage{msg: msg}
}

// Data returns the raw message payload.
func (m *natsMessage) Data() []byte {
	return m.msg.Data()
}

// Subject returns the message subject.
func (m *natsMessage) Subject() string {
	return m.msg.Subject()
}

// Ack acknowledges successful processing.
func (m *natsMessage) Ack() error {
	return m.msg.Ack()
}

// Nak signals processing failure, requesting redelivery.
func (m *natsMessage) Nak() error {
	return m.msg.Nak()
}

// NakWithDelay requests redelivery after a delay.
func (m *natsMessage) NakWithDelay(delay time.Duration) error {
	return m.msg.NakWithDelay(delay)
}

// Term terminates the message (no redelivery).
func (m *natsMessage) Term() error {
	return m.msg.Term()
}

// Metadata returns delivery metadata.
func (m *natsMessage) Metadata() (pubsub.MessageMetadata, error) {
	md, err := m.msg.Metadata()
	if err != nil {
		return pubsub.MessageMetadata{}, err
	}
	return pubsub.MessageMetadata{
		NumDelivered: md.NumDelivered,
		Timestamp:    md.Timestamp,
		Subject:      m.msg.Subject(),
		Stream:       md.Stream,
		Consumer:     md.Consumer,
	}, nil
}
