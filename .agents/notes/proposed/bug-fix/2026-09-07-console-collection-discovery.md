# Agent Note: Console Collection Discovery

Status: proposed

## Problem

[Collection discovery](../../../../packages/console/src/lib/documents.ts) sends `collection: ''` to the document query endpoint, samples at most 100 documents, and converts every error into an empty list. [Query validation](../../../../packages/syntrix/internal/gateway/rest/validation.go) requires a valid collection path, so this call cannot provide the collection tree. Sampling would also miss collections even if the endpoint allowed it.

The [collection tree](../../../../packages/console/src/components/features/data-browser/CollectionTree.tsx) calls `onSelectDatabase(data.databases[0].display_name)` and routes later requests using that label. Display names are presentation data; the database API resolves identifiers. These are confirmed static integration defects. The normal query helper already consumes the backend's bare document array correctly.

## Proposal

Define and implement a paginated, authorized collection-list operation scoped to a canonical database identifier. Return collection paths independently of document sampling, with a documented policy for nested paths and collections containing only tombstones. Use storage collection metadata or an authoritative catalog after measuring enumeration cost; expose failures explicitly rather than treating them as no collections.

Keep the selected database's stable identifier separate from its display label throughout the tree and document browser. On database changes, clear collection and document selections, cancel stale requests, and reject late responses belonging to the previous selection. Requests must preserve database identity even when display names are duplicated or renamed.

The backend must define who may enumerate collection names. Its authorization must prevent revealing paths beyond the caller's permitted discovery scope; document reads remain subject to their own rules.

## Alternatives

**Manual collection-path entry:** avoids an enumeration API and remains useful for direct navigation, but does not fulfill the tree discovery workflow.

**Scanning documents:** reuses query execution, but requires unbounded reads for completeness, handles empty collections poorly, and unnecessarily exposes document data. Metadata enumeration is proposed because names are the required result.

## Acceptance Criteria

- More than 100 documents spread across multiple collections do not cause any discoverable collection to disappear from the completed paginated list.
- Duplicate and renamed database display labels never change the database addressed by CRUD operations.
- Permission denial, backend failure, and an empty database produce distinct UI states.
- Rapid database switching cannot show or mutate the previous database's selected document; nested and tombstone-only collection behavior matches the documented policy.

## Risks

Maintaining a catalog adds write and migration costs; querying metadata directly may be expensive at scale. The storage choice must preserve completeness and database isolation without changing ordinary query response formats.
