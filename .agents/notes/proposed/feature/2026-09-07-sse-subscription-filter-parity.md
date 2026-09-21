# Agent Note: SSE Subscription Filter Parity

Status: proposed

## Problem

The [realtime design](../../../../docs/design/server/gateway/realtime_watching.md)
intends SSE and WebSocket to share authentication and subscription semantics.
The transports exist, but [the SSE handler](../../../../internal/gateway/realtime/client.go)
registers without filters and includes document data. It has no snapshot option.
[SDK SSE options](../../../../sdk/syntrix-client-ts/src/replication/realtime-sse.ts)
expose only collection and an injected fetch implementation, while
[ordinary server WebSocket options](../../../../internal/gateway/realtime/protocol.go)
include a query, `includeData`, and `sendSnapshot`. SDK 的公开 WS client 已移除；此提案
保留普通协议的 SSE parity 目标，不将私有 replica-data 当作普通 WS 的兼容包装。

Static inspection identifies incomplete transport parity. Changing transport
changes the predicate and initial-data options an application can express.

## Proposal

Use a shared validated subscription description: collection, filters, data
inclusion, and snapshot request. Keep one subscription per SSE connection and
encode it in a documented GET query format with a strict size limit and typed
values. Continue header authentication without credentials in URLs. Validate
before opening the stream and pass the complete description through the same
subscription and snapshot services used by WebSocket.

Expose the description in the SDK SSE client and preserve it for caller-driven
reconnection. Validate collections, operators, value types, and size consistently.
Omitted options use documented shared defaults; malformed fields return a
structured error without creating subscriptions. Changing filters opens a new
subscription with an explicit lifecycle.

Transport the membership contract owned by
[filtered snapshots](../bug-fix/2026-09-07-realtime-filtered-snapshots.md): entries,
updates, and removals for filter exits or deletes, plus explicit resynchronization
when membership cannot be determined. Preserve identities and reasons even when
`includeData` is false. The SDK removes a filter-exiting document from the watched
result without requesting a storage delete. SSE and WS must produce the same
invalidation outcome when membership or handoff continuity is lost. Parity does
not independently guarantee delivery after disconnect; the linked resume
proposal owns replay and cursor handling.

Keep heartbeat comments and cancellation. Registration failure or disconnect
releases client and Streamer subscriptions. Use the shared message schema for
snapshot completion, membership changes, and structured failures; diagnostics
record subscription correlation and error category without filter values or
document bodies. Update SDK/reference examples with the final encoding and
contract in the implementing change.

## Alternatives

**POST the description and consume its response stream.** The fetch-based SDK
can avoid URL limits and logging exposure, at the cost of another method beyond
the current GET interface. Reconsider if required filters exceed the GET bound
or require URL confidentiality.

**Create a subscription resource before GET.** This shortens URLs but adds
ownership, expiry, and cleanup across two requests. The current connection
lifecycle does not require that additional state.

## Acceptance Criteria

- Equivalent WS and SSE subscriptions produce the same entries, updates, filter
  exits, and delete removals for supported predicates in each database.
- `includeData=false` retains removal identities and reasons; snapshot and
  membership failures produce the same visible resynchronization outcome.
- A snapshot member exiting its filter or being deleted during handoff cannot
  remain silently synchronized in either transport.
- Invalid types, operators, oversized queries, and unauthorized databases fail
  without allocating subscriptions.
- Disconnect, failed registration, and reconnect release resources and retain
  the requested predicate; header authentication remains required as configured.

## Risks

GET parameters can appear in access logs and have size limits. Deferral keeps
transport selection visible in application behavior. A shared description and
membership schema preserve parity as validation and protocol options evolve;
sharing filters alone would preserve stale-result defects in both transports.

## Dependencies

The linked filtered snapshot proposal owns snapshot and membership correctness.
[Client resume](2026-09-07-realtime-client-resume.md) owns SSE event IDs, replay
cursors, and resynchronization across reconnects, independently of encoding.
