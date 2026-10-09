# Agent Note: Retire the Orphan Public Go Service Scaffold

Status: proposed

## Problem

The public [service package](../../../../packages/syntrix/pkg/syntrix/service.go) declares an empty
`type Service interface {}` and returns an empty implementation from NewService.
Its [test](../../../../packages/syntrix/pkg/syntrix/service_test.go) only checks that the returned
value is non-nil. Repository import searches found no caller of this package.
Meanwhile, the actual [server entry point](../../../../packages/syntrix/cmd/syntrix/main.go)
constructs packages/syntrix/internal/services.Manager and invokes Init, Start, and Shutdown.
Server startup has an implementation; the unrelated public scaffold creates an
unclear second service entry point without any usable contract.

## Proposal

Retire the empty package and its construction-only test after checking repository
imports, examples, build targets, and release documentation for public promises.
Keep the working command and service-manager lifecycle as the documented server
entry point. Remove stale references to the empty package if any are found;
do not replace it with another unused interface.

If a public embedding requirement is identified before removal, record that
consumer and define a separate proposal for an actual Go API: configuration
ownership, startup failures, readiness, cancellation, shutdown deadlines, and
which implementation details remain private. A meaningful API should be driven
by that use case rather than inferred from an empty constructor. If an external
release promised this package, removal must follow an explicit API-breaking
release decision and migration guidance; it must not silently leave a no-op
compatibility wrapper.

## Alternatives

**Keep the scaffold as a reservation.** This preserves an import path but gives
callers no operations and maintainers no criteria for completing it. A path can
be introduced when a concrete public contract exists.

**Turn it into an embedding facade now.** This could support applications that
host the server, but no repository consumer establishes that requirement. It
would expose lifecycle obligations and maintenance costs without evidence of
demand. Reconsider when a named consumer needs embedding.

## Acceptance Criteria

- Repository searches find no imports or build references to the retired package;
  active documentation identifies the actual supported server entry point.
- Server builds and existing lifecycle tests pass after package removal, and the
  normal startup path continues to initialize the same service manager.
- The release-contract check records whether an external API promise exists; any
  required removal notice and migration instructions ship with the change.
- No replacement empty interface, no-op wrapper, or construction-only test remains.

## Risks

Repository searches cannot establish whether external consumers import a public
package. Release history and published API commitments must be checked before
removal. Delaying removal costs little runtime work but preserves a misleading
public API until its ownership is settled.
