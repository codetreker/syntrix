# Agent Notes

An Agent Note records a decision or proposal: the problem, why a particular
choice was made, the alternatives considered, and what the choice costs.

## Document ownership

| Location | Responsibility |
|---|---|
| `docs/design/` | Requirements and architecture; explain How and the Why needed to understand it |
| `docs/reference/` | Observable API and SDK behavior |
| `docs/plans/` | Local, gitignored execution steps for an authorized task |
| `docs/tasks/` | Retained task and validation reports |
| `.agents/notes/` | Durable decisions, detailed alternatives, consequences, and conditions for reconsideration |

Use the confirmed task and the relevant existing documents as requirements.
Notes do not introduce a separate requirements hierarchy. Keep concise rationale
beside the mechanism in design discussions, and link to the note for the full
decision. A note does not replace an execution plan or validation evidence.

## Paths and lifecycle

Use `{lifecycle}/{class}/yyyy-mm-dd-topic-title.md`. The date is when the topic
was first proposed; moving or revising a note preserves it. Create a directory
when its first note needs it. The directory tree is the inventory; do not keep
a second centralized note index.

| Lifecycle | Meaning |
|---|---|
| `proposed/` | Substantial work deliberately deferred; work already being implemented skips this state |
| `implemented/` | A decision reflected in the delivered changes; current system facts stay synchronized |
| `rejected/` | A declined proposal, retained while its rationale prevents a plausible mistake |
| `archived/` | An implemented decision whose rationale no longer needs active maintenance |

Classes are `feature`, `bug-fix`, `simplification`, `architecture`, `process`,
and `testing`. `architecture` concerns the structure and contracts of delivered
source code; `process` concerns the tools and workflow around it.

Use relative Markdown links between notes and to their owning documents. Moving
a note requires updating active inbound links in the same change. Frozen
archives may intentionally retain historical references.

## When a note is required

A nontrivial change includes an implemented note or updates its existing owning
note in the same change as the implementation, related documents, and validation.
Nontrivial changes affect behavior, architecture, shared contracts, tooling,
testing strategy, stored or wire formats, or a decision worth revisiting.
Purely mechanical or local edits are exempt.

Read-only investigations remain read-only unless recording a proposal is part
of the authorized scope. Do not invent a proposal solely to satisfy this format.

Before creating a note, search for an existing owner of the decision or mechanism.
Update that note for the same decision. If a decision replaces an earlier one,
write a new note and cross-link the active notes. Partial supersession keeps both
notes, with their remaining responsibilities made clear.

## Format

The opening is:

```markdown
# Agent Note: <Title>

Status: implemented
```

Active status lines are `Status: proposed`, `Status: implemented`, or
`Status: rejected - <one-line reason>`, matching the directory. An archived
note retains `Status: implemented`; its path records archival, and its contents
are frozen.

Start with `## Problem`, describing a problem that makes sense independently
of the chosen solution. Use these sections:

| Lifecycle | Required sections |
|---|---|
| Proposed | Problem, Proposal, Alternatives, Acceptance Criteria, Risks |
| Implemented | Problem, Decision, Alternatives, Consequences |
| Rejected | Preserve the proposal's sections and add the rejection reason to its status |
| Archived | Preserve the implemented note unchanged |

Additional technical sections may be inserted where needed. An implemented
note describes the delivered decision in present tense; remove proposal plans
and fold acceptance results, risks, and trade-offs into Consequences.

`## Alternatives` is mandatory. Record only choices actually considered, what
each offered, and the concrete reason it was not selected. If no alternatives
were considered, say so. Do not invent weaker choices to justify a decision.
An option that remains open should state what would make it worth reconsidering.

## Updating and superseding

Rewrite a changed note as a coherent document. Keep these two kinds of statement
distinct:

- **Current facts:** paths, package names, defaults, and descriptions of the
  current system. Synchronize these with the implementation.
- **Decision history:** what was known, compared, chosen, and accepted at that
  time. Preserve these facts even when a later decision changes the system.

Never rewrite a note into the opposite decision. A replacement gets a new note;
cross-links explain how it relates to the old one. Links to changed decisions
are integrated into the document, not appended as contradictory corrections.

For `proposed/` to `implemented/`, move the file, change Status, rewrite Proposal
as Decision, fold acceptance and risks into Consequences, and remove execution
plans. Describe the actual result without retaining abandoned implementation
steps.

For `proposed/` to `rejected/`, move the file and change only Status to include
the reason. The proposal remains the record of what was declined. An obsolete
proposal is rejected, never archived.

## Retention

Keep an implemented note active while its alternatives, ownership boundaries,
guarantees, data semantics, constraints, or reconsideration conditions still
guide work. Archive it only when the decision is complete and that rationale
no longer needs active maintenance.

After archival, do not edit, move, delete, or treat the note as evidence of current
behavior. New active documents may link to it as history; superseding decisions
link back without changing the archive.

Delete a rejected note only when its rationale no longer prevents a meaningful
mistake. Delete an active implemented note only when it is fully superseded,
the owning note absorbs every unique rationale, alternative, consequence, and
named gap, and active inbound links are repaired. Git history alone is not a
replacement for accessible rationale.

## Language and checks

Write notes in English. The [agent-notes skill](../skills/agent-notes/SKILL.md)
provides the editing workflow. Check status/path agreement, mandatory sections,
relative links, and supersession references after changes. Validate only active
notes against current facts; archived contents remain historical.
