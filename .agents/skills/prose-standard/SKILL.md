---
name: prose-standard
description: Use when writing or reviewing repository prose, including documents, decision notes, comments, commit messages, diagnostics, and CLI strings. Preserves complete contracts while removing repetition and authoring-session narration.
---

# Prose Standard

Write enough to preserve the contract, then remove reasoning transcripts, repetition, and decoration. A contract is an obligation, invariant, precondition, postcondition, or compatibility promise relied on by a caller, callee, implementer, producer, or consumer.

Use [trim-cot-leakage](../trim-cot-leakage/SKILL.md) for authoring-session narration specifically. Repository documents, notes, skills, and code are written in English; communicate with the user in Chinese.

## Preserve the complete proposition

Before editing, identify the relevant actor and action; condition, timing, and ordering; modality such as must, may, and never; negative guarantee and exception; ownership, side effect, failure mode, and consequence.

Remove words only when every relevant factual clause survives and the result is clearer. A lower word count alone is not an improvement. Never turn a proposal, assumption, or unverified result into a shipped fact.

Keep the complete local contract at the point of use, including the behavior, failure, ownership, and consequence needed there. Give extended architecture, rationale, algorithms, history, and examples one owning document and link to it. Essential contract facts may repeat locally. Preserve non-obvious rationale when its omission could cause misuse or an incorrect simplification.

Add or restore prose when code, types, and structure do not communicate a required contract. Do not add comments for facts already obvious locally.

## Match the document's purpose

| Location | Required content |
|---|---|
| `docs/design/` | Requirements, structure, component contracts, data flows, and costs appropriate to the document. Keep a concise Why alongside How and link to the durable decision note for full alternatives and rationale. Clearly distinguish delivered behavior from proposals. |
| `docs/reference/` | Consumer-visible APIs, configuration, guarantees, limits, and failure behavior. Separate supported behavior from intended behavior. |
| Local `docs/plans/` | Confirmed scope, remaining work, dependencies, and observable acceptance or completion evidence. Keep the execution plan accurate. |
| Existing `docs/tasks/` reports | Retained task and validation history; preserve the recorded context and outcomes. |
| README documents | Purpose, supported usage, operational prerequisites, and links to owning documentation. Preserve the local contract needed to use the component. |
| `.agents/notes/` | Unique rationale, mechanisms, real alternatives, consequences, and named gaps. Active implemented notes keep current facts accurate while preserving historical decision context. |
| Research and evidence documents | Resolvable sources, dates or revisions when material, methods, observed results, and the distinction between evidence and inference. Synchronize active documents; only explicit historical archives are frozen. |
| Exported identifiers | Caller-visible return distinctions, errors, side effects, ownership, timing, cancellation, and durability. |
| Internal comments | Non-local structure, invariants, ordering hazards, ownership, security rules, and surprising failure behavior. |
| Package comments | Role, dependencies, responsibilities, and non-obvious structural choices, linked to their durable owner when needed. |
| Tests | Non-obvious fixture choices, indirect observations, or platform accommodations. |
| Diagnostics and error strings | Failing subject, violated rule, and correction when non-obvious. |

Preserve searchable mechanism names and meaningful modal, temporal, or negative emphasis. Terms such as “contract,” “boundary,” “shape,” “surface,” and “invariant” are useful when they name the technical subject; replace them with the actual rule or operation when they only decorate a sentence.

## Comments

Comments explain non-obvious contracts or rationale that code cannot express; they do not narrate control flow. Write as the system's author, using “we,” “our,” or passive voice. Do not mention authoring-session participants, reviews, or sessions.

Never cite an uncommitted draft, review artifact, or temporary document. Use a durable committed reference or state the necessary rationale inline. Keep the evidence chain for non-obvious researched decisions, using commit-pinned source links where appropriate.

## Workflow

1. Use the user's stated scope and existing authorization. Do not infer a repository-wide rewrite from a local request or ask to reconfirm an already explicit scope.
2. Read the applicable `AGENTS.md` and the document that owns the subject before judging a passage.
3. Inspect the whole requested scope. Search for candidates, then judge them semantically.
4. Classify passages as keep, add, trim, restore, restructure, or defer. Edit only within authorized scope; never manufacture edits to meet a deletion target.
5. Update the owning document first, then synchronize related active documents. Rewrite edited documents into a coherent final state, preserving historical archives.
6. Report the inspected scope, material changes, and unresolved decisions or deferred work.

A borderline decision has at least two versions that preserve every relevant proposition while trading accepted principles. When such a choice materially affects the requested result, present viable versions with a recommendation and their factual or structural difference. Do not weaken a proposition or invent an inferior option to create a choice.
