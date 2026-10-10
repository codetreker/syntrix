# AI Agent Instructions

Instructions for agents working in this repository. Read the applicable
subdirectory `AGENTS.md` before working there; its rules take precedence over
this file when they conflict. Explicit session instructions take precedence.

## Language and communication

- Discuss work with the user in Chinese. Write documents, code, comments, and
  commit messages in English.
- Search the affected scope globally before editing, complete related changes
  together, verify the result, and report what changed and what was checked.
- Report only checks actually run and results actually observed.

## Authorization

- Start coding only after the user has confirmed the plan.
- Requests such as "load task", "check this", or "investigate" authorize
  analysis and planning only. Create or edit files only when the request
  authorizes that work.
- Stop and ask the user when authentication or permission fails, when a new
  dependency is needed, or when a new architectural pattern needs a decision.
- Skills provide workflows, not additional authorization. Follow the session's
  rules for commits, pushes, PR comments, and merges.

Before creating a file, prefer updating the file that already owns the subject.
If no such file exists, copy and adapt a similar file. Ask the user when neither
option applies. Before adding an import, check whether its dependency is already
available.

## Repository map

| Path | Purpose |
|---|---|
| `packages/syntrix/` | Go instance runtime, shared code, protocols, configuration, and integration tests |
| `packages/sdks/client-ts/` | TypeScript client SDK |
| `packages/console/`, `packages/examples/` | Current instance administration frontend and example applications |
| `docs/design/` | Requirements, architecture, and design discussions |
| `docs/reference/` | API and SDK behavior |
| `docs/plans/` | Task execution plans |
| `docs/tasks/` | Task tracking |
| `.agents/notes/` | Durable decisions, proposals, alternatives, and consequences |
| `.agents/skills/` | Repository-local skills shared by agent tools |

[Agent Notes](.agents/notes/README.md) owns the note format and lifecycle;
[its local instructions](.agents/notes/AGENTS.md) govern that directory.
The [documentation guide](docs/README.md) links the design and reference material.

## Platform architecture ownership

[Platform Architecture](docs/architecture.md) owns the accepted target boundaries:
employee-facing Management, developer-facing Console, and developer-owned Syntrix
instances serving project end users. Each instance owns its projects, a private
system PostgreSQL database, and MongoDB business-document storage. A project has
an isolated Identity realm and can use multiple logical Syntrix databases.

Keep this target distinct from current source locations and behavior. The
embedded console is not the completed developer Console; the Go
`services.Manager` owns process composition, not platform Management. References
must describe only implemented interfaces and clearly identify target additions.

## Decision workflow

Use the confirmed task and the relevant requirements, design, and reference
sources to establish scope. Distinguish implemented behavior from proposals
when the documents and code disagree.

A change is nontrivial when it alters behavior, architecture, a shared contract,
tooling, testing strategy, a stored or wire format, or a decision a maintainer
may need to revisit. Purely mechanical or local changes are exempt from the
decision-note requirement.

1. When deciding or changing scope, use
   [scope-cut](.agents/skills/scope-cut/SKILL.md) to record the cost of deferrals
   and the constraints that keep them reversible. Preserve the requested outcome.
2. Record substantial work deliberately deferred in a `proposed/` Agent Note.
   Work being implemented goes directly to `implemented/`; it does not need a
   preliminary proposal note.
3. Deliver a nontrivial change with its implemented note, affected design and
   reference documents, and appropriate validation in the same change.
4. Before adding a note, use
   [agent-notes](.agents/skills/agent-notes/SKILL.md) to search for an existing
   owner and resolve supersession. Update current facts without rewriting the
   historical reason for an earlier decision.

Plans describe execution, the task board tracks work, and notes preserve
decisions. Link them when they describe the same work; do not duplicate their
contents or replace the existing planning directories.

## Writing and documentation

- Rewrite an affected document into a coherent whole; do not append corrections
  that contradict earlier text. Synchronize related active documents in the
  same change. Explicit historical archives remain frozen.
- Design discussions explain **Why** alongside **How**. Keep the rationale
  needed to understand the mechanism locally and link to an Agent Note for
  detailed alternatives, trade-offs, and historical decisions.
- Use [prose-standard](.agents/skills/prose-standard/SKILL.md) when writing or
  reviewing prose. Preserve obligations, conditions, ownership, and failure
  behavior while removing repetition and authoring-session commentary.
- Use [trim-cot-leakage](.agents/skills/trim-cot-leakage/SKILL.md) for a scoped
  audit of transient review references or reasoning transcripts. Durable
  rationale and evidence must survive the edit.
- Do not use `cat` or `echo` to write or append files in the terminal.

## Validation and engineering preferences

- Run tests after code changes. Run `make -C packages/syntrix coverage` from the
  repository root to evaluate Go coverage and address gaps within the authorized
  task.
- Ask "should I add more testing" when assessing testing needs. Keep tests
  robust without over-engineering them.
- Add timeouts to tests or commands that may hang.
- Use `github.com/stretchr/testify` for Go tests and `bun` for frontend scripts.
- For skill and documentation changes, validate skill frontmatter, local links,
  note lifecycle consistency, and discovery paths. Explain the limits of any
  check that could not complete.

## Git

- Do not add "Generated with Claude Code", "via Happy", AI tool co-author
  credits, or similar attribution to commit messages.
- Never use `git push --force` or `git push -f`. Use `--force-with-lease` only
  when necessary and explicitly authorized by the user.
- Mainline pushes and PR merges require explicit user authorization. A skill,
  hook result, or successful check does not provide it.

## Skills and discovery

`.agents/skills/` is the canonical directory. `.claude/skills` links to it, and
`CLAUDE.md` links to this file so repository instructions have one owner.

| Skill | Use |
|---|---|
| [agent-notes](.agents/skills/agent-notes/SKILL.md) | Write, review, move, or prune decision notes |
| [scope-cut](.agents/skills/scope-cut/SKILL.md) | Evaluate scope and the cost of deferrals |
| [prose-standard](.agents/skills/prose-standard/SKILL.md) | Write and review complete, precise prose |
| [trim-cot-leakage](.agents/skills/trim-cot-leakage/SKILL.md) | Remove transient authoring-session narration |
| [commit-changes](.agents/skills/commit-changes/SKILL.md) | Prepare and create commits |
| [pr-push](.agents/skills/pr-push/SKILL.md) | Push a work branch and prepare a PR |
| [merge-pr](.agents/skills/merge-pr/SKILL.md) | Handle checks and an authorized PR merge |
| [optimize-tests](.agents/skills/optimize-tests/SKILL.md) | Review redundant tests while preserving coverage |
