# Agent Note: Shared Agent Workflows and Decision Records

Status: implemented

## Problem

Repository skills need a discoverable owner shared by coding tools. Decisions
also need a durable home for alternatives and consequences, connected to the
design discussions and execution plans that use them.

The existing project conventions require English documents, execution plans in
`docs/plans/`, and Why alongside How in design discussions. A reusable workflow
must fit those conventions so it does not create competing sources of policy.

## Decision

`.agents/skills/` owns eight skills. Four cover decision notes, scope decisions,
prose quality, and removal of transient authoring-session narration. The four
existing commit, PR, merge, and test-optimization skills live in the same
directory. `.claude/skills` is a relative symlink to it; `CLAUDE.md` links to the
root [instructions](../../../../AGENTS.md).

[Agent Notes](../../README.md) use lifecycle and class directories. Substantial
deferred work is proposed; decisions implemented in the current change go
directly into implemented notes. Historical decision circumstances remain intact
while descriptions of current system facts track subsequent code changes.

Design discussions retain concise rationale and link to notes for detailed
alternatives. Local plans describe execution; existing task reports preserve
validation history. Project task tracking does not use a repository Markdown
board. All imported skills and note conventions use English and refer to the
existing documentation layout.

## Alternatives

**Keep separate skill copies for each tool.** This preserves independent tool
directories, but every revision then needs duplicate edits and can leave the
tools following different instructions. A shared directory has one owner.

**Copy the source workflow and its records without adaptation.** This preserves
the original layout exactly, but its language, filesystem-specific requirements,
and historical decisions describe another project. Only the reusable conventions
fit this repository; they require its document paths and language rules.

**Keep all rationale in design documents.** That preserves the existing Why/How
practice, but detailed alternatives and superseded decisions need a lifecycle
separate from descriptions of the current mechanism. Notes provide that history
while design discussions retain the rationale needed locally.

## Consequences

Both discovery paths resolve to the same skills. Moving an existing skill does
not change its instructions. Relative links and symlinks keep the workflow
independent of a developer's checkout path.

Nontrivial changes carry a decision record and synchronized documentation, which
adds maintenance work. Supersession searches and explicit archive rules keep
those records useful. Checkouts must preserve symlinks to use the Claude aliases;
the canonical `.agents/skills/` files and `AGENTS.md` remain directly accessible.

Skill validation checks metadata, discovery paths, and relative links. It does
not establish that the application services pass their runtime tests.
