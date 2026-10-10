# Task Board

Owner identifies the role responsible for the next action. Every task has an
owner: backlog, ready, discussion, review, and done use 飞马; implementation uses
战马; acceptance uses 烈马. Local execution plans remain in the gitignored
`docs/plans/` directory; tracked decisions and task state identify delivered and
remaining work.

| ID | Task | Status | Owner | PR |
|---|---|---|---|---|
| SYN-001 | UI presentation design | backlog | 飞马 | — |
| SYN-002 | Unified Puller subscription state machine | In Review | 飞马 | [#167](https://github.com/codetreker/syntrix/pull/167) |
| SYN-003 | [Instance Identity extraction](../../.agents/notes/proposed/architecture/2026-10-10-platform-console-instance-boundaries.md): Gateway document authorization | In Review | 飞马 | — |

SYN-003 starts with Gateway document-policy ownership. Account contracts and
Gateway authentication adapters, signing/verification capabilities, and Identity
repository/composition ownership follow in separate dependent PRs after this
step merges. Project realms, PostgreSQL revocation/sessions, independent
deployment, and both OAuth roles remain subsequent capabilities with their own
activation contracts.
