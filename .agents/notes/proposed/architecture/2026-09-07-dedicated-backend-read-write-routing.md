# Agent Note: Read/Write Topology for Dedicated Database Backends

Status: proposed

## Problem

Shared document/revocation topology already supports `read_write_split` through
the [split routers](../../../../packages/syntrix/internal/core/storage/router/split.go). Dedicated
bindings use a single backend field in
[storage configuration](../../../../packages/syntrix/internal/core/storage/config/config.go), and
the [factory](../../../../packages/syntrix/internal/core/storage/factory_impl.go) constructs
`router.NewSingleDocumentRouter(store)` for each dedicated document binding.
The [multi-database design](../../../../docs/design/server/core/storage/05.multi-database.md)
explicitly leaves dedicated read/write strategy open. This is a planned topology
extension; shared splitting and database-specific routing already exist.

## Proposal

Represent each dedicated document and revocation binding with an explicit
operation topology using the existing `single` or `read_write_split` strategies,
primary/replica backend names, and collection settings. Resolve database ownership
first, then select its operation-specific store through the existing routers.
Share validation/construction with the default topology so configuration cannot
describe a strategy that assembly silently ignores.

Specify which operations need primary consistency, including conditional writes,
watch streams, and immediate revocation checks. Replica reads must document their
staleness contract; the extension must not introduce an automatic fallback to an
unrelated backend when a configured replica fails. User records remain in their
existing global PostgreSQL store.

Migrate existing dedicated bindings to explicit single topologies in deployment
configuration. Validate referenced backends and collection mappings before opening
services, and close partially initialized providers on failure. Backend relocation
and copying existing data are separate operations; changing a binding must not
claim to migrate its contents.

## Alternatives

**Let each MongoDB connection manage its own replica topology.** This remains useful
when driver configuration provides the required consistency, but does not expose
the existing application-level primary/replica contract per database.

**Apply one global split strategy to every binding.** This reduces configuration
fields but cannot represent databases with different dedicated replica placement
or a deliberate single-backend deployment.

## Acceptance Criteria

- Two dedicated databases and a shared database independently route each supported
  operation to their configured primary/replica without crossing database data.
- Explicit single topology preserves current dedicated behavior; shared topology
  continues using its configured strategy.
- Invalid or incomplete bindings fail startup with the database and backend name;
  partial initialization closes owned resources.
- Replica outage, cancellation, and primary-consistency operations follow the
  documented error/consistency contract without silently changing destinations.
- Configuration migration is explicit and diagnostics exclude backend credentials.

## Risks

Replica staleness affects query expectations and revocation safety. More topology
configuration adds deployment validation work. Deferral limits dedicated placement
to one configured backend; reusing operation routers and explicit database
identity preserves a straightforward extension path.
