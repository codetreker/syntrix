# Agent Note: Apply Trigger Pubsub Configuration at Runtime

Status: proposed

## Problem

[Service assembly](../../../../packages/syntrix/internal/services/manager_init.go) creates trigger
publishers and consumers with stream and subject names only. The evaluator and
delivery [factory helpers](../../../../packages/syntrix/internal/trigger/evaluator/factory.go)
carry storage, retry, and consumer-name settings, but that configuration mapping
is bypassed by assembly. [The NATS consumer](../../../../packages/syntrix/internal/core/pubsub/nats/consumer.go)
uses memory storage and the durable name `consumer` when those options are absent.
Consequently, configured file storage and delivery identity do not reach the
active distributed path. This conclusion is based on static call tracing.

## Proposal

Give each trigger service one configuration-to-pubsub-options mapping and use it
from runtime assembly and service factory entry points. Pass evaluator storage
and publish retry settings, delivery storage and consumer identity, and the
relevant queue bounds explicitly. Validate that publisher and consumer settings
for a shared stream agree before connecting either service.

Make deployment-mode semantics explicit: standalone uses the existing in-memory
provider and its documented process lifetime; distributed mode applies the
configured NATS storage policy. Do not treat an embedded-NATS scaffold as an
active runtime requirement.

Treat stream storage and durable consumer name changes as operations with
persistent state. On startup, report a conflict with the existing broker resource
rather than silently changing its retention or creating an unintended independent
consumer. Document drain, resource migration, and checkpoint coordination when
operators intentionally change them. Log effective non-secret stream, consumer,
and storage settings so startup evidence can be compared with configuration.

## Alternatives

**Call the existing NATS factory helpers directly** reuses their option mapping,
but bypasses the provider abstraction used by standalone mode and service tests.
Extracting the mapping retains both configuration ownership and provider choice.

**Document broker defaults as the effective behavior** removes some settings,
but abandons the configured durability and consumer identity controls rather than
completing their integration.

## Acceptance Criteria

- Manager-level assembly checks observe every supported setting in the resulting
  publisher/consumer options, including non-default values.
- A distributed service creates or attaches to the declared durable consumer and
  storage policy; a file-backed queue retains accepted work across broker restart.
- Conflicting stream settings or existing resource policies fail startup with an
  actionable resource identifier and no silent policy mutation.
- Standalone retains its documented in-memory behavior and never requires NATS.

## Risks

Existing streams may require migration before corrected file-storage settings
can apply. Renaming a durable consumer can replay work or leave a backlog owned
by the old consumer. Delivery correctness also depends on
[publish/checkpoint ordering](2026-09-07-trigger-publish-checkpoint-ordering.md).
