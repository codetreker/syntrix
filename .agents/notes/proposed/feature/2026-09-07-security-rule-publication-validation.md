# Agent Note: Complete Security Rule Publication Validation

Status: proposed

## Problem

Rules already undergo YAML parsing, database/path consistency checks, and CEL
compilation. In the [validator](../../../../packages/syntrix/internal/core/identity/authz/engine.go),
`env.Compile(cleanCondition)` checks expressions recursively. It does not apply
the additional publication limits and ambiguity checks described in the
[authorization design](../../../../docs/design/server/core/identity/03.authorization.md).
That design also records that current path ordering uses an alphabetical
tie-breaker, while declaration-order semantics are planned. These are incomplete
publication guarantees established by static inspection, not reproduced runtime
incidents.

## Proposal

Create one bounded validation pipeline used by file loading, publication,
dry-run, and rollback eligibility. Enforce the documented 256 KiB source limit,
AST-depth limit of 20, five referenced `get()`/`exists()` calls per rule, and
bounded match/allow cardinalities. Validate tail-only recursive globs, supported
CEL operations, boolean conditions, and same-database absolute lookup paths.
Reject expressions whose lookup scope cannot be established under the supported
language contract; runtime evaluation remains responsible for dynamic values.

Compile an ordered internal rule representation from the source so declaration
order survives parsing. Specify overlap checks against the intended precedence
rules and reject equally specific, conflicting matches. Return structured
diagnostics with source location, rule path, and violated limit. The same source
must produce the same result on every node.

Changing tie-breaking requires an explicit rule-language revision and a migration
report comparing decisions for existing rule fixtures. Activation remains owned
by the publication lifecycle; validation never partially installs a ruleset.

## Alternatives

**Retain alphabetical tie-breaking.** This avoids an ordering migration but changes
the documented intended contract; it remains viable only if that requirement is
deliberately revised.

**Rely on evaluation-time rejection.** Runtime limits remain necessary, but they
cannot provide deterministic publication diagnostics or prevent oversized source
from consuming compilation resources.

## Acceptance Criteria

- Boundary fixtures for every limit accept the maximum valid value and reject
  the next invalid value through every loading/publication entry point.
- Ambiguous, malformed, cross-database, and non-boolean rules fail before activation.
- Equivalent inputs produce identical ordering and diagnostics across processes.
- Existing valid rules receive a migration result identifying changed decisions;
  failed migration leaves the active revision intact.
- Error reporting identifies the failing rule without including secret values
  or document data.

## Risks

Stricter validation may reject previously accepted files. Documenting the supported
language and requiring explicit migration avoids silent authorization changes.
Deferral leaves the published limits unenforced; preserving source and language
revision keeps future checks reproducible.

## Dependencies

[Security rule publication](../architecture/2026-09-07-security-rule-publication-lifecycle.md)
owns durable versions, dry-run execution, and activation.
