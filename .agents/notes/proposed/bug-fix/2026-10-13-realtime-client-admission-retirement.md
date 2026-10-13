# Agent Note: Realtime Client Admission and Backend Retirement

Status: proposed

## Problem

A legacy realtime connection can subscribe to an old backend before the Hub has
installed that connection in its active-client map. Backend replacement removes
the subscription mapping and closes the installed-client snapshot. A delayed
registration can then install the dependent client after that snapshot, leaving
its transport open without its previous subscription.

The existing registration sequence permits this interleaving:

1. `Hub.Register` returns after handing the client to the unbuffered request
   channel, before `Hub.Run` inserts it under `h.mu`.
2. `ServeWs` starts the client's pumps after that admission returns.
3. `SubscribeToStream` and `RegisterSubscription` accept an old-stream
   subscription without checking active-client membership.
4. Stream replacement clears subscription mappings and snapshots active clients.
5. The registration receiver resumes and installs the missed client.

At commit `611a72046fabde827417a0ee2cf7522b46f0b4b9`,
[registration admission](https://github.com/codetreker/syntrix/blob/611a72046fabde827417a0ee2cf7522b46f0b4b9/packages/syntrix/internal/gateway/realtime/hub.go#L315-L322),
[map insertion](https://github.com/codetreker/syntrix/blob/611a72046fabde827417a0ee2cf7522b46f0b4b9/packages/syntrix/internal/gateway/realtime/hub.go#L89-L92),
[pump startup](https://github.com/codetreker/syntrix/blob/611a72046fabde827417a0ee2cf7522b46f0b4b9/packages/syntrix/internal/gateway/realtime/client.go#L515-L523),
and [retirement snapshot](https://github.com/codetreker/syntrix/blob/611a72046fabde827417a0ee2cf7522b46f0b4b9/packages/syntrix/internal/gateway/realtime/hub.go#L425-L436)
show these boundaries. A controlled race-enabled schedule paused the registration
receiver between handoff and insertion, accepted an old-stream subscription,
replaced the stream, and then resumed insertion. The subscription mapping was
removed while the client's outbound lifecycle remained open.

## Proposal

Make ordinary-client admission and retirement agree on when a client becomes
active and which backend owns its subscriptions. Inspect the existing Hub
registration path before selecting a handshake or shared synchronization rule.
A successful admission must not permit an old-dependent client to miss retirement;
cancellation must settle any admitted request without leaving an orphaned client.

This runtime change is separate from release publication, which does not alter
Hub admission. Deferral requires a later audit of WS/SSE startup, registration,
subscription, retirement, and shutdown; another timeout does not repair the
ordering. The release pipeline introduces no competing registration mechanism,
so that audit does not require reworking publication. Established-client fixtures
must continue waiting for map membership before testing their retirement case.

## Alternatives

**Wait for membership only in an established-client fixture.** This establishes
that fixture's prerequisite and preserves its closure assertions. It does not
protect production subscriptions admitted before installation.

**Implement registration changes with release publication.** Publication does
not own these runtime synchronization rules. Combining them would require a
separate caller and cancellation audit inside an otherwise independent workflow
change. Keep the runtime repair independently reviewable.

## Acceptance Criteria

- A deterministic handoff/insertion/retirement regression proves an old-dependent
  connection is closed or rejected; a missed subscription cannot leave it open.
- Ordinary WS/SSE startup, backend replacement, and owner cancellation leave no
  admitted but unowned client and do not deadlock the Hub.
- Existing replica retirement, new-backend subscriptions, and established-client
  closure assertions continue passing under race detection.

## Risks

Acknowledging registration adds a shutdown settlement obligation. Shared
synchronization must preserve lock ordering with stream and subscription owners.
Until repaired, the controlled interleaving can leave an ordinary connection open
after its old subscription mapping has been removed.
