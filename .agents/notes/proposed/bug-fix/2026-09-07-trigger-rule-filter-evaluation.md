# Agent Note: Evaluate Every Configured Trigger Filter

Status: proposed

## Problem

[Trigger](../../../../packages/syntrix/internal/trigger/types/types.go) exposes `Filters []string`,
but [the CEL evaluator](../../../../packages/syntrix/internal/trigger/evaluator/cel/evaluator.go)
checks database, event type, collection, and `Condition` only. A rule containing
filters can therefore publish tasks without applying them. Repository inspection
finds no defined syntax or combination semantics for this string list; the
meaning proposed below is a contract decision, not existing behavior.

## Proposal

Define each filter string as a CEL boolean predicate over the same `event`
environment used by `Condition`. A rule matches when its database, event type,
and collection match, every filter is true, and its optional condition is true.
An empty filter list imposes no extra predicate. This gives the existing field a
precise meaning without introducing a second expression language.

Compile and type-check conditions and filters before replacing the active rule
set. Reject invalid syntax and expressions whose result is not boolean. Cache
compiled programs by their expression and environment version, preserving bounded
cache behavior. Evaluate against one immutable event projection, with consistent
missing-field and before-image semantics. Evaluation errors must remain visible
and follow the evaluator's failed-event policy; they must not turn into a match
or an unreported false result.

Document the distinction from structured document-query filters and include
examples combining `filters` with `condition`. This change does not alter query
filter operators elsewhere in the API.

## Alternatives

**Remove `filters` and require `condition`** eliminates duplicate ways to express
conjunction, but requires an explicit rule-file migration and loses independently
named list entries for generated configuration. It remains viable if maintainers
choose one expression field as the public contract.

**Introduce structured query filters** allows sharing query tooling but changes
the current string-list format and may not express event metadata or before/after
comparisons. It needs a separate format decision.

## Acceptance Criteria

- A false filter prevents publication even when the condition matches; all-true
  filters and an absent condition match only the selected database and collection.
- Invalid and non-boolean predicates reject the candidate rule set while the
  previously active set remains usable.
- Missing fields, absent before images, and evaluation failures follow documented
  semantics and identify the trigger and predicate without exposing payload data.
- Reload and restart preserve identical evaluation behavior for the same rules.

## Risks

Giving a formerly ignored field meaning changes delivery volume. Existing rule
files require an explicit validation and migration review before activation.
[Publish/checkpoint ordering](2026-09-07-trigger-publish-checkpoint-ordering.md)
owns the policy that prevents failed evaluation from silently advancing progress.
