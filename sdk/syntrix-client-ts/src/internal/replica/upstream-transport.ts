import type { AxiosInstance, AxiosRequestConfig } from 'axios';
import { AuthSessionChangedError, SyntrixError } from '../../api/errors.js';
import { decodeQueryValue, encodeQueryValue, type QueryValue, type TypedValue } from '../../api/value.js';
import type { TokenProvider } from '../auth/types.js';
import { ReplicaStorageError } from './storage-types.js';
import type { PreparedPushBatch, PushConflict, PushOperation, RemoteDocument, UpstreamRequestContext, UpstreamTransport } from './upstream-types.js';

export const pushRequestBytes = 10 * 1024 * 1024;
export const pushProtobufBytes = 20 * 1024 * 1024;
// Gateway limits protobuf conflicts, not their flattened JSON representation.
// This is a client resource ceiling; exceeding it leaves the write uncertain.
export const pushResponseBytes = 32 * 1024 * 1024;
const queryResponseBytes = 16 * 1024 * 1024;
const queryRequestBytes = 1024 * 1024;
const maxVersion = (1n << 63n) - 1n;
const utf8 = new TextEncoder();
const protectedFields = new Set(['id', 'collection', 'version', 'createdAt', 'updatedAt', 'deleted']);
export type UpstreamFailureDisposition = 'not-dispatched' | 'not-executed' | 'unknown';
const failureDispositions = new WeakMap<object, UpstreamFailureDisposition>();
export const upstreamFailureDisposition = (error: unknown): UpstreamFailureDisposition =>
  typeof error === 'object' && error !== null ? failureDispositions.get(error) ?? 'unknown' : 'unknown';
const fail = (message: string): never => { throw new ReplicaStorageError('ReplicaUpstreamInvalid', message); };
const size = (text: string): number => utf8.encode(text).byteLength;
const object = (value: unknown): Record<string, unknown> => {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return fail('Expected an upstream object');
  return value as Record<string, unknown>;
};
const exact = (value: Record<string, unknown>, fields: readonly string[]): void => {
  if (Object.keys(value).length !== fields.length || fields.some(field => !Object.prototype.hasOwnProperty.call(value, field))) fail('Unexpected upstream response fields');
};
const text = (value: unknown): value is string => {
  if (typeof value !== 'string' || !value || value.includes('\0')) return false;
  try { encodeQueryValue(value); return true; } catch { return false; }
};
const logicalId = (value: unknown): value is string => text(value) && !value.includes('/');
const freeze = <T>(value: T): T => {
  if (typeof value === 'object' && value !== null) {
    Object.values(value).forEach(freeze);
    Object.freeze(value);
  }
  return value;
};
const parse = (value: unknown, maxBytes: number): unknown => {
  if (typeof value !== 'string') return fail('Upstream transport must return JSON text');
  if (value.length > maxBytes || size(value) > maxBytes) {
    throw new ReplicaStorageError('ReplicaUpstreamResponseTooLarge', 'Upstream response exceeds its resource budget');
  }
  try { return JSON.parse(value); } catch (cause) {
    throw new ReplicaStorageError('ReplicaUpstreamInvalid', 'Upstream response is not valid JSON', { cause });
  }
};
const decodeDocument = (value: unknown, id: string, collection: string): RemoteDocument => {
  let decoded: QueryValue;
  try { decoded = decodeQueryValue(value); } catch (cause) {
    throw new ReplicaStorageError('ReplicaUpstreamInvalid', 'Invalid typed upstream document', { cause });
  }
  const doc = object(decoded);
  if (!logicalId(doc.id) || doc.id !== id || doc.collection !== collection ||
      ('deleted' in doc && typeof doc.deleted !== 'boolean')) return fail('Upstream document identity or deletion state differs');
  for (const field of ['version', 'createdAt', 'updatedAt']) {
    if (typeof doc[field] !== 'bigint') return fail(`Upstream ${field} requires int64 metadata`);
  }
  if ((doc.version as bigint) < 0n) return fail('Upstream version must be nonnegative');
  if (doc.deleted === true && Object.keys(doc).some(key => !protectedFields.has(key))) return fail('Upstream tombstone contains business payload');
  return doc as RemoteDocument;
};
const decodeConflicts = (raw: unknown, batch: PreparedPushBatch, collection: string): { conflicts: PushConflict[] } => {
  const result = object(raw);
  exact(result, ['conflicts']);
  if (!Array.isArray(result.conflicts) || result.conflicts.length > batch.changes.length) return fail('Invalid upstream conflicts');
  let previous = -1;
  const conflicts = result.conflicts.map(value => {
    const conflict = object(value);
    exact(conflict, ['changeIndex', 'id', 'reason', 'current']);
    const index = conflict.changeIndex;
    if (typeof index !== 'number' || !Number.isSafeInteger(index) || index <= previous || index >= batch.changes.length) return fail('Invalid upstream conflict index');
    previous = index;
    const change = batch.changes[index]!;
    if (conflict.id !== change.logicalId) return fail('Upstream conflict ID differs from the submitted change');
    const current = conflict.current === null ? null : decodeDocument(conflict.current, change.logicalId, collection);
    const live = current !== null && current.deleted !== true;
    let valid = false;
    switch (conflict.reason) {
      case 'missing': valid = current === null; break;
      case 'tombstoned': valid = current?.deleted === true; break;
      case 'version_mismatch': valid = live && change.baseVersion !== undefined && current.version !== change.baseVersion; break;
      case 'already_exists': valid = live && change.action === 'create'; break;
      case 'precondition_failed': valid = live && (change.baseVersion === undefined || current.version === change.baseVersion); break;
    }
    if (!valid) return fail('Upstream conflict reason contradicts the submitted change or current state');
    return { changeIndex: index, id: change.logicalId, reason: conflict.reason as PushConflict['reason'], current };
  });
  return { conflicts };
};
const varintSize = (value: number): number => {
  let bytes = 1;
  while (value >= 128) { value = Math.floor(value / 128); bytes++; }
  return bytes;
};
const fieldBytes = (bytes: number): number => 1 + varintSize(bytes) + bytes;
const stringField = (value: string): number => value ? fieldBytes(size(value)) : 0;
const floats = (node: TypedValue): number => node.type === 'float64' ? 1 :
  node.type === 'object' ? Object.values(node.value).reduce((sum, child) => sum + floats(child), 0) :
  node.type === 'array' ? node.value.reduce((sum, child) => sum + floats(child), 0) : 0;
const protobufChangeBytes = (database: string, collection: string, change: PushOperation, data: TypedValue): number => {
  // Match the StoredDoc envelope in query.proto without computing either hash.
  // Go JSON escapes HTML and Unicode separators; float formatting reserves an
  // extra 16 bytes per value beyond the JS spelling. Int64 fields reserve ten.
  const encoded = JSON.stringify(data);
  let escapingBytes = 0;
  for (let index = 0; index < encoded.length; index++) {
    const code = encoded.charCodeAt(index);
    if (code === 60 || code === 62 || code === 38) escapingBytes += 5;
    else if (code === 0x2028 || code === 0x2029) escapingBytes += 3;
  }
  const dataBytes = size(encoded) + escapingBytes + 16 * floats(data);
  const parent = collection.includes('/') ? collection.slice(0, collection.lastIndexOf('/')) : '';
  const docBytes = stringField(`${database}:${'0'.repeat(32)}`) + stringField(database) +
    stringField(`${collection}/${change.logicalId}`) + stringField(collection) + stringField('0'.repeat(32)) +
    stringField(parent) + 11 + 11 + 2 + fieldBytes(dataBytes) + (change.action === 'delete' ? 2 : 0);
  const changeBytes = fieldBytes(docBytes) + (change.baseVersion === undefined ? 0 : 11) + 2;
  return fieldBytes(changeBytes);
};

export const createReplicaHttpUpstream = (options: {
  axios: AxiosInstance; provider: TokenProvider; database: string; collection: string;
}): UpstreamTransport => {
  const { axios, provider, database, collection } = options;
  if (!text(database) || !text(collection) || collection.split('/').some(segment => !segment || segment.includes('*')) ||
      collection.split('/').length % 2 !== 1) return fail('Invalid upstream database or concrete collection');
  const prepared = new WeakSet<PreparedPushBatch>();
  const assert = (context: UpstreamRequestContext): void => {
    context.signal.throwIfAborted();
    if (provider.getSessionVersion() !== context.sessionVersion) throw new AuthSessionChangedError();
    if (!/^[0-9a-f]{16}$/.test(context.expectedDatabaseIdentity)) fail('A bound database identity is required');
  };
  const config = (context: UpstreamRequestContext, maxBytes: number): AxiosRequestConfig & { _syntrixAuthSessionVersion: number } => ({
    signal: context.signal, _syntrixAuthSessionVersion: context.sessionVersion,
    responseType: 'text', maxContentLength: maxBytes,
    headers: { 'Content-Type': 'application/json', 'X-Syntrix-Expected-Database-Identity': context.expectedDatabaseIdentity },
    transformResponse: [(data: unknown, _headers: unknown, status?: number) => {
      if (status !== undefined && (status < 200 || status >= 300)) {
        if (typeof data === 'string' && data.length === 0) return data;
        try { return parse(data, maxBytes); } catch (error) {
          if (error instanceof ReplicaStorageError && error.code === 'ReplicaUpstreamResponseTooLarge') throw error;
          return data;
        }
      }
      return data;
    }],
  });
  return {
    prepare(changes) {
      if (!Array.isArray(changes)) return fail('Push changes must be an array');
      const baseProto = stringField(database) + stringField(collection);
      const entries = changes.map((input, index) => {
        if (!input || !['create', 'update', 'delete'].includes(input.action) || !logicalId(input.logicalId)) return fail('Invalid push action or document ID');
        if (!/^[a-zA-Z0-9_.-]{1,64}$/.test(input.logicalId)) {
          throw new ReplicaStorageError('ReplicaPushUnsupportedID', 'HTTP Push requires a document ID of 1–64 ASCII letters, digits, underscore, period, or hyphen');
        }
        if (input.action === 'create' ? input.baseVersion !== undefined :
          typeof input.baseVersion !== 'bigint' || input.baseVersion < 0n || input.baseVersion > maxVersion) return fail('Create omits version; update and delete require a nonnegative int64 version');
        const payload = object(input.payload);
        if (Object.keys(payload).some(key => protectedFields.has(key))) return fail('Push business payload contains protected metadata');
        if (input.action === 'delete' && Object.keys(payload).length) return fail('Push deletion must omit business payload');
        const business = encodeQueryValue({ ...payload, id: input.logicalId });
        const snapshot = freeze({ logicalId: input.logicalId, action: input.action,
          payload: decodeQueryValue(encodeQueryValue(payload)) as PushOperation['payload'],
          ...(input.baseVersion === undefined ? {} : { baseVersion: input.baseVersion }) });
        const document = encodeQueryValue({ ...payload, id: input.logicalId,
          ...(input.baseVersion === undefined ? {} : { version: input.baseVersion }) });
        const encoded = JSON.stringify({ action: input.action, document });
        return { index, snapshot, encoded, proto: protobufChangeBytes(database, collection, snapshot, business) };
      });
      const batches: PreparedPushBatch[] = [];
      const prefix = `{"collection":${JSON.stringify(collection)},"changes":[`;
      const envelopeBytes = size(prefix) + 2;
      let group: typeof entries = [], httpBytes = envelopeBytes, protoBytes = baseProto;
      const finish = () => {
        if (!group.length) return;
        const batch = freeze({ body: prefix + group.map(entry => entry.encoded).join(',') + ']}',
          changes: group.map(entry => entry.snapshot), indexes: group.map(entry => entry.index), httpBytes, protobufBytes: protoBytes });
        prepared.add(batch); batches.push(batch);
        group = []; httpBytes = envelopeBytes; protoBytes = baseProto;
      };
      for (const entry of entries) {
        const entryBytes = size(entry.encoded);
        if (envelopeBytes + entryBytes > pushRequestBytes || baseProto + entry.proto > pushProtobufBytes) {
          throw new ReplicaStorageError('ReplicaPushRequestTooLarge', 'One push change exceeds the HTTP or protobuf request budget');
        }
        if (group.length === 50 || httpBytes + entryBytes + (group.length ? 1 : 0) > pushRequestBytes || protoBytes + entry.proto > pushProtobufBytes) finish();
        httpBytes += entryBytes + (group.length ? 1 : 0); protoBytes += entry.proto; group.push(entry);
      }
      finish();
      return Object.freeze(batches);
    },
    async push(batch, suppliedContext) {
      const context = { ...suppliedContext };
      let dispatched = false;
      try {
        if (!prepared.has(batch)) return fail('Push batch does not belong to this transport');
        assert(context);
        await context.beforeDispatch?.();
        assert(context);
        dispatched = true;
        const response = await axios.post(`/replication/v1/databases/${encodeURIComponent(database)}/push`, batch.body, config(context, pushResponseBytes));
        assert(context);
        return decodeConflicts(parse(response.data, pushResponseBytes), batch, collection);
      } catch (error) {
        let disposition: UpstreamFailureDisposition = dispatched ? 'unknown' : 'not-dispatched';
        if (dispatched && error instanceof SyntrixError &&
            ((error.status === 409 && error.code === 'DATABASE_IDENTITY_MISMATCH') ||
              (error.status === 401 && error.code === 'UNAUTHORIZED') ||
              (error.status === 403 && error.code === 'FORBIDDEN') ||
              (error.status === 429 && error.code === 'RATE_LIMITED'))) disposition = 'not-executed';
        if (typeof error === 'object' && error !== null) {
          // Cancellation owners can reuse an error across sibling callbacks.
          // A later undispatched sibling cannot erase earlier uncertainty.
          const previous = failureDispositions.get(error);
          failureDispositions.set(error, previous === 'unknown' ? previous : disposition);
        }
        throw error;
      }
    },
    async readCurrent(id, suppliedContext) {
      const context = { ...suppliedContext };
      assert(context);
      if (!logicalId(id)) return fail('Invalid authoritative-read document ID');
      const body = JSON.stringify({ collection, filters: [{ field: 'id', op: '==', value: encodeQueryValue(id) }], limit: 1, showDeleted: true });
      if (size(body) > queryRequestBytes) throw new ReplicaStorageError('ReplicaPushRequestTooLarge', 'Authoritative query exceeds its request budget');
      const response = await axios.post(`/api/v1/databases/${encodeURIComponent(database)}/query`, body, config(context, queryResponseBytes));
      assert(context);
      const page = object(parse(response.data, queryResponseBytes));
      exact(page, ['documents', 'nextCursor', 'effectiveOrder']);
      if (!Array.isArray(page.documents) || page.documents.length > 1 || page.nextCursor !== null ||
          !Array.isArray(page.effectiveOrder) || page.effectiveOrder.length !== 1) return fail('Authoritative ID query is not a complete single-ID result');
      const order = object(page.effectiveOrder[0]);
      exact(order, ['field', 'direction']);
      if (order.field !== 'id' || order.direction !== 'asc') return fail('Authoritative ID query has an unexpected order');
      return page.documents.length ? decodeDocument(page.documents[0], id, collection) : null;
    },
  };
};
