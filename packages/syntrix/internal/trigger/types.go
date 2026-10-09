package trigger

import (
	"github.com/codetreker/syntrix/internal/trigger/types"
)

type Trigger = types.Trigger
type RetryPolicy = types.RetryPolicy
type DeliveryTask = types.DeliveryTask
type Evaluator = types.Evaluator
type EventPublisher = types.EventPublisher
type Duration = types.Duration
type FatalError = types.FatalError
type SecretProvider = types.SecretProvider
type Metrics = types.Metrics

var IsFatal = types.IsFatal
