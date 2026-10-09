import type { QueryOrder } from '../../api/types.js';
import { decodeQueryValue } from '../../api/value.js';
import { ReplicaStorageError, type FrozenSourceDefinition } from './storage-types.js';
import type { SourceDocument, SourceEvent, SourceEventsPage, SourceReadContext, SourceWindow } from './source-types.js';

export const sourcePageBytes = 16 * 1024 * 1024;
export const sourcePageCount = 100;
const checkpointBytes = 256 * 1024;
const utf8 = new TextEncoder();
const invalid = (message: string): never => { throw new ReplicaStorageError('ReplicaSourceInvalid', message); };
const object = (value: unknown): Record<string, unknown> => {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return invalid('Expected source object');
  return value as Record<string, unknown>;
};
const exact = (value: Record<string, unknown>, fields: string[]): void => {
  if (Object.keys(value).length !== fields.length || fields.some(field => !Object.prototype.hasOwnProperty.call(value, field))) {
    invalid('Source object has missing or unsupported fields');
  }
};
const validText = (value: unknown): value is string => {
  if (typeof value !== 'string' || value.length === 0 || value.includes('\0')) return false;
  for (let i = 0; i < value.length; i++) {
    const unit = value.charCodeAt(i);
    if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(++i);
      if (!(next >= 0xdc00 && next <= 0xdfff)) return false;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) return false;
  }
  return true;
};
const logicalId = (value: unknown): string => {
  if (!validText(value) || value.includes('/')) return invalid('Invalid source logical document ID');
  return value;
};
const checkpoint = (value: unknown): string => {
  if (!validText(value) || value.length > checkpointBytes || utf8.encode(value).byteLength > checkpointBytes) {
    return invalid('Source checkpoint must be a nonempty string of at most 256 KiB');
  }
  return value;
};
const document = (value: unknown, collection: string): SourceDocument => {
  let decoded: unknown;
  try { decoded = decodeQueryValue(value); } catch (cause) {
    throw new ReplicaStorageError('ReplicaSourceInvalid', 'Invalid typed source document', { cause });
  }
  const doc = object(decoded);
  logicalId(doc.id);
  if (doc.collection !== collection || ('deleted' in doc && doc.deleted !== false)) {
    return invalid('Source upsert must be live and belong to the requested collection');
  }
  for (const field of ['version', 'createdAt', 'updatedAt']) {
    if (typeof doc[field] !== 'bigint') return invalid(`Source ${field} must be int64 metadata`);
  }
  if ((doc.version as bigint) < 0n) return invalid('Source version must be nonnegative');
  return doc as SourceDocument;
};
const event = (value: unknown, collection: string): SourceEvent => {
  const entry = object(value);
  if (entry.type === 'upsert') {
    exact(entry, ['type', 'document']);
    return { type: 'upsert', document: document(entry.document, collection) };
  }
  if (entry.type === 'leave' || entry.type === 'delete') {
    exact(entry, ['type', 'id']);
    return { type: entry.type, id: logicalId(entry.id) };
  }
  return invalid('Unknown source event type');
};

const assertPageBytes: (text: unknown) => asserts text is string = text => {
  if (typeof text !== 'string') return invalid('Source transport must return JSON text');
  // Reject before UTF-8 allocation and typed decoding. The HTTP adapter also
  // enforces maxContentLength where streaming transport limits are supported.
  if (text.length > sourcePageBytes || utf8.encode(text).byteLength > sourcePageBytes) {
    throw new ReplicaStorageError('ReplicaSourceResponseTooLarge', 'Source response exceeds 16 MiB');
  }
};

export const parseSourceEnvelope = (text: unknown): unknown => {
  assertPageBytes(text);
  try { return JSON.parse(text); } catch (cause) {
    throw new ReplicaStorageError('ReplicaSourceInvalid', 'Source response is not valid JSON', { cause });
  }
};

const decodeEnvelope = (
  value: unknown, definition: FrozenSourceDefinition, context: SourceReadContext,
): SourceEventsPage | SourceWindow => {
  const page = object(value);
  const mode = definition.limit === null ? 'events' : 'replace';
  exact(page, mode === 'events'
    ? ['protocolVersion', 'mode', 'databaseIdentity', 'sourceHash', 'events', 'checkpoint', 'generationId', 'phase', 'caughtUp', 'bootstrapComplete']
    : ['protocolVersion', 'mode', 'databaseIdentity', 'sourceHash', 'requestId', 'generationId', 'complete', 'effectiveOrder', 'documents']);
  if (page.protocolVersion !== 1 || page.mode !== mode ||
      typeof page.databaseIdentity !== 'string' || !/^[0-9a-f]{16}$/.test(page.databaseIdentity) ||
      typeof page.sourceHash !== 'string' || !/^[0-9a-f]{64}$/.test(page.sourceHash) ||
      typeof page.generationId !== 'string' ||
      !/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/.test(page.generationId) ||
      page.generationId === '00000000-0000-0000-0000-000000000000') {
    return invalid('Invalid source protocol, mode, or identity');
  }
  if (context.expectedDatabaseIdentity !== null && page.databaseIdentity !== context.expectedDatabaseIdentity) {
    throw new ReplicaStorageError('ReplicaIdentityMismatch', 'Source database identity differs from the bound database');
  }
  if (context.expectedSourceHash !== null && page.sourceHash !== context.expectedSourceHash) {
    throw new ReplicaStorageError('ReplicaSourceMismatch', 'Source hash differs from the bound query');
  }
  const identity = { protocolVersion: 1 as const, databaseIdentity: page.databaseIdentity, sourceHash: page.sourceHash, generationId: page.generationId };
  if (mode === 'events') {
    if (!Array.isArray(page.events) || page.events.length > sourcePageCount ||
        (page.phase !== 'scan' && page.phase !== 'replay' && page.phase !== 'live') ||
        typeof page.caughtUp !== 'boolean' || typeof page.bootstrapComplete !== 'boolean' ||
        (page.phase !== 'live' && (page.caughtUp || page.bootstrapComplete)) ||
        (page.phase === 'live' && !page.bootstrapComplete)) {
      return invalid('Invalid source event progress or count');
    }
    return { ...identity, mode, checkpoint: checkpoint(page.checkpoint), phase: page.phase,
      caughtUp: page.caughtUp, bootstrapComplete: page.bootstrapComplete,
      events: page.events.map(entry => event(entry, definition.collection)) };
  }
  if (page.complete !== true || page.requestId !== context.requestId || !validText(page.requestId) ||
      !Array.isArray(page.documents) || definition.limit === null || page.documents.length > definition.limit ||
      !Array.isArray(page.effectiveOrder)) return invalid('Invalid complete source window');
  const expectedOrder = definition.orderBy.some(order => order.field === 'id')
    ? definition.orderBy : [...definition.orderBy, { field: 'id', direction: 'asc' as const }];
  if (page.effectiveOrder.length !== expectedOrder.length) return invalid('Source window order differs from the query');
  const effectiveOrder = page.effectiveOrder.map((value, index) => {
    const order = object(value);
    exact(order, ['field', 'direction']);
    if (order.field !== expectedOrder[index]!.field || order.direction !== expectedOrder[index]!.direction) {
      return invalid('Source window order differs from the query');
    }
    return { ...expectedOrder[index]! };
  });
  const ids = new Set<string>();
  const documents = page.documents.map(value => {
    const doc = document(value, definition.collection);
    if (ids.has(doc.id)) return invalid('Source window has duplicate logical IDs');
    ids.add(doc.id);
    return doc;
  });
  return { ...identity, mode, requestId: page.requestId, complete: true, effectiveOrder, documents };
};

export const decodeSourceResponse = (
  text: string, definition: FrozenSourceDefinition, context: SourceReadContext,
): SourceEventsPage | SourceWindow => decodeEnvelope(parseSourceEnvelope(text), definition, context);

export const decodeSourcePage = (
  value: unknown, definition: FrozenSourceDefinition, context: SourceReadContext,
): SourceEventsPage | SourceWindow => {
  // A WS frame has a separate envelope allowance. Check its page independently
  // before creating bigint values or decoded business objects.
  const text = JSON.stringify(value);
  assertPageBytes(text);
  return decodeEnvelope(value, definition, context);
};

export type WireSourceDefinition = {
  version: 1;
  filters: FrozenSourceDefinition['filters'];
  orderBy: QueryOrder[];
  limit?: number;
};
export type WirePullRequest = {
  collection: string;
  source: WireSourceDefinition;
} & ({ checkpoint: string | null; limit: number } | { requestId: string });

export const copySourceDefinition = (input: FrozenSourceDefinition): FrozenSourceDefinition => {
  const definition = structuredClone(input);
  if (!validText(definition.collection) ||
      (definition.limit !== null && (!Number.isInteger(definition.limit) || definition.limit < 1 || definition.limit > 1000))) {
    return invalid('Invalid source collection or window limit');
  }
  const freeze = (value: unknown): void => {
    if (typeof value !== 'object' || value === null) return;
    Object.values(value).forEach(freeze);
    Object.freeze(value);
  };
  freeze(definition);
  return definition;
};

export const validateSourceDatabase = (database: string): void => {
  if (!validText(database)) invalid('Invalid source database');
};

export const encodeSourceDefinition = (definition: FrozenSourceDefinition): WireSourceDefinition => ({
  version: 1, filters: definition.filters, orderBy: definition.orderBy,
  ...(definition.limit === null ? {} : { limit: definition.limit }),
});

export const encodeSourceRequest = (definition: FrozenSourceDefinition, context: SourceReadContext): WirePullRequest => {
  if (!validText(context.requestId)) return invalid('Source request identity is required');
  if (context.expectedDatabaseIdentity !== null && !/^[0-9a-f]{16}$/.test(context.expectedDatabaseIdentity)) return invalid('Invalid bound database identity');
  if (context.expectedSourceHash !== null && !/^[0-9a-f]{64}$/.test(context.expectedSourceHash)) return invalid('Invalid bound source hash');
  const source = encodeSourceDefinition(definition);
  return definition.limit === null
    ? { collection: definition.collection, source, checkpoint: context.checkpoint === null ? null : checkpoint(context.checkpoint), limit: sourcePageCount }
    : { collection: definition.collection, source, requestId: context.requestId };
};
