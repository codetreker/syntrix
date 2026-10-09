# Agent Note: Deliver Complete Trigger Event Envelopes

Status: proposed

## Problem

[DeliveryTask](../../../../packages/syntrix/internal/trigger/types/types.go) declares `lsn`, `seq`,
`before`, `after`, and `ts`, but
[task construction](../../../../packages/syntrix/internal/trigger/evaluator/service.go) supplies
only identity, routing, execution settings, and `Payload`. The worker serializes
the task directly, leaving declared event metadata at zero values or absent.
Receivers cannot reliably identify or interpret the source event. This is a
static producer-to-consumer finding.

## Proposal

Define one versioned delivery envelope and build it from the canonical source
event before queueing. Include the source event ID, logical database, collection,
document ID, event type, evaluated trigger version, ingestion timestamp in Unix
milliseconds, and explicit before/after document semantics. Preserve stable source
identity through replay and retries; never use publish or HTTP-attempt time as
the source timestamp.

The existing `lsn` and `seq` fields have no established producer mapping. Replace
these unpopulated placeholders in the proposed schema with `eventId` and a
`sourcePosition` containing backend identity and source cluster time. Document
ordering only within the source stream that defines it; an aggregated recovery
token does not establish a global event sequence. Remove the duplicate generic
`payload` representation when introducing this schema, updating SDK consumers,
fixtures, and reference examples together.

Keep create/update/soft-delete document semantics explicit: create has an after
image, update has an after image, and logical delete has no live after image.
Previous data is emitted only under the before-image contract. Separate delivery
configuration from the public event fields so internal routing fields do not
accidentally become the receiver API.

Drain or explicitly migrate queued tasks during schema deployment; reject an
unsupported envelope version rather than inventing missing source metadata.

## Alternatives

**Populate `lsn` and `seq` without changing names** avoids a format rename, but
requires authoritative definitions and source mappings first. It is viable if
existing integrations depend on those names and their semantics can be specified
without implying a global order.

**Keep only `payload`** preserves current document access but leaves event
identity, previous state, and timestamps unavailable to receivers.

## Acceptance Criteria

- A matched source event produces a schema-valid envelope with nonempty stable
  event identity, correct database/document identity, and the source timestamp.
- Live, replayed, and retried deliveries preserve event fields exactly; attempt
  signatures and attempt metadata remain separate.
- Create, update, and soft-delete fixtures verify image presence and omission.
- SDK decoding and receiver examples match the wire schema; unsupported queued
  versions fail visibly under the documented deployment procedure.

## Risks

The schema change affects webhook consumers and durable queues. Source metadata
must survive local and remote event conversion. Before images depend on
[capture and retention](../feature/2026-09-07-trigger-before-images.md);
[idempotency](../architecture/2026-09-07-trigger-delivery-idempotency.md) consumes
the stable event and rule identity defined here.
