package nats

import (
	"context"
	"errors"

	pubsubconfig "github.com/codetreker/syntrix/internal/core/pubsub/config"
	"github.com/nats-io/nats.go/jetstream"
)

func attach(ctx context.Context, js JetStream, binding pubsubconfig.BrokerBinding, allowCreate bool) (jetstream.Consumer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if js == nil {
		return nil, errors.New("JetStream is required")
	}
	spec, err := binding.Specification()
	if err != nil {
		return nil, err
	}
	create := allowCreate && binding.ResourceMode == "operator"
	stream, err := js.Stream(ctx, spec.Stream.Name)
	if errors.Is(err, jetstream.ErrStreamNotFound) && create {
		_, createErr := js.CreateStream(ctx, spec.Stream)
		stream, err = js.Stream(ctx, spec.Stream.Name)
		if err != nil {
			return nil, &OperationError{Operation: "stream creation", Cause: errors.Join(createErr, err)}
		}
	}
	if err != nil {
		return nil, &OperationError{Operation: "stream lookup", Cause: err}
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return nil, &OperationError{Operation: "stream inspection", Cause: err}
	}
	if err := verifyStreamSpecification(spec, info.Config); err != nil {
		return nil, err
	}
	consumer, err := js.Consumer(ctx, spec.Stream.Name, spec.Consumer.Name)
	if errors.Is(err, jetstream.ErrConsumerNotFound) && create {
		_, createErr := js.CreateConsumer(ctx, spec.Stream.Name, spec.Consumer)
		consumer, err = js.Consumer(ctx, spec.Stream.Name, spec.Consumer.Name)
		if err != nil {
			return nil, &OperationError{Operation: "consumer creation", Cause: errors.Join(createErr, err)}
		}
	}
	if err != nil {
		return nil, &OperationError{Operation: "consumer lookup", Cause: err}
	}
	infoConsumer, err := consumer.Info(ctx)
	if err != nil {
		return nil, &OperationError{Operation: "consumer inspection", Cause: err}
	}
	if err := verifyConsumerSpecification(spec, infoConsumer.Config); err != nil {
		return nil, err
	}
	return consumer, nil
}
