import { decodeQueryValue, encodeQueryValue, type QueryValue } from '../../api/value.js';
import type { FilterOp } from '../../api/types.js';
import {
  ReplicaStorageError, type AliasManifest, type DataRecord, type FrozenSourceDefinition,
  type ReplicaCondition, type ReplicaDocument, type ReplicaRecord, type ReplicaSourceDefinition,
  type MemberRecord, type WireMetadata,
} from './storage-types.js';

const reserved = new Set(['id', 'collection', 'version', 'createdAt', 'updatedAt', 'deleted']);
const systems = new Set(['_rev', '_meta', '_attachments', '_deleted']);
const operators = new Set<FilterOp>(['==', '!=', '>', '>=', '<', '<=', 'in', 'contains']);
const encoder = new TextEncoder();
const int64Min = -(1n << 63n);
const int64Max = (1n << 63n) - 1n;
const corruption: (message: string) => never = (message) => { throw new ReplicaStorageError('ReplicaStorageCorruption', message); };
const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value) &&
  (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null);
const own = (value: object, key: string) => Object.prototype.hasOwnProperty.call(value, key);
const string = (value: unknown): value is string => typeof value === 'string' && value.length > 0;
const nullableString = (value: unknown) => value === null || string(value);

export const canonicalJson = (value: unknown): string => {
  const ancestors = new Set<object>();
  const encode = (node: unknown): string => {
    if (node === null || typeof node === 'boolean') return JSON.stringify(node);
    if (typeof node === 'number') {
      if (!Number.isFinite(node)) throw new TypeError('Canonical JSON requires finite numbers');
      return JSON.stringify(node);
    }
    if (typeof node === 'string') {
      encodeQueryValue(node);
      return JSON.stringify(node);
    }
    if (!Array.isArray(node) && !object(node)) throw new TypeError('Canonical JSON requires JSON values');
    if (ancestors.has(node)) throw new TypeError('Canonical JSON cannot contain cycles');
    if (Object.getOwnPropertySymbols(node).length) throw new TypeError('Canonical JSON requires string keys');
    ancestors.add(node);
    try {
      if (Array.isArray(node)) return `[${Array.from(node, encode).join(',')}]`;
      return `{${Object.keys(node).sort().map(key => `${encode(key)}:${encode(node[key])}`).join(',')}}`;
    } finally { ancestors.delete(node); }
  };
  return encode(value);
};

const deepFreeze = <T>(value: T): T => {
  if (value !== null && typeof value === 'object') {
    Object.values(value).forEach(deepFreeze);
    Object.freeze(value);
  }
  return value;
};

export const validateLogicalId: (id: unknown) => asserts id is string = (id) => {
  if (!string(id) || /[\/\0]/.test(id)) throw new TypeError('Logical document ID must be a nonempty path segment');
  encodeQueryValue(id);
};

const sha256 = async (value: string): Promise<string> => {
  const bytes = await crypto.subtle.digest('SHA-256', encoder.encode(value));
  return Array.from(new Uint8Array(bytes), byte => byte.toString(16).padStart(2, '0')).join('');
};

export const recordKey = async (kind: 'd' | 'm', logicalId: string): Promise<string> => {
  if (kind !== 'd' && kind !== 'm') throw new TypeError('Only data and member records have logical IDs');
  validateLogicalId(logicalId);
  return `${kind}:${await sha256(logicalId)}`;
};

export const encodeBusinessPayload = (value: unknown): string => {
  if (!object(value)) throw new TypeError('Document data must be a plain object');
  for (const key of Object.keys(value)) {
    if (reserved.has(key)) throw new TypeError(`Document field ${key} is read-only`);
  }
  return canonicalJson(encodeQueryValue(value));
};

export const decodeBusinessPayload = (payload: string): Record<string, QueryValue> => {
  try {
    const value = decodeQueryValue(JSON.parse(payload));
    if (!object(value) || encodeBusinessPayload(value) !== payload) {
      return corruption('Business payload must be a canonical typed object');
    }
    return value as Record<string, QueryValue>;
  } catch (error) {
    if (error instanceof ReplicaStorageError) throw error;
    throw new ReplicaStorageError('ReplicaStorageCorruption', 'Invalid business payload', { cause: error });
  }
};

export const validateField: (field: unknown) => asserts field is string = (field) => {
  if (!string(field) || /[.\p{Cc}]/u.test(field)) throw new TypeError('Query fields must be literal nonempty top-level names');
  encodeQueryValue(field);
};
const scalar = (value: QueryValue) => value === null || ['boolean', 'string', 'number', 'bigint'].includes(typeof value);
const numeric = (value: QueryValue): value is number | bigint => typeof value === 'number' || typeof value === 'bigint';
export const equalValue = (a: QueryValue, b: QueryValue): boolean => {
  if (!scalar(a) || !scalar(b)) return false;
  // ECMAScript's mixed BigInt/Number relational comparison is exact and does
  // not round the BigInt through Number, including values beyond 2^53.
  if (numeric(a) && numeric(b)) return !(a < b) && !(a > b);
  return a === b;
};

export const frozenConditions = (conditions: readonly ReplicaCondition[]): readonly ReplicaCondition[] => {
  if (!Array.isArray(conditions)) throw new TypeError('Conditions must be an array');
  return deepFreeze(conditions.map(condition => {
    if (!object(condition) || Object.keys(condition).some(key => !['field', 'op', 'value'].includes(key))) {
      throw new TypeError('Invalid condition');
    }
    validateField(condition.field);
    if (!operators.has(condition.op as FilterOp)) throw new TypeError('Invalid condition operator');
    const value = decodeQueryValue(encodeQueryValue(condition.value));
    switch (condition.op) {
      case 'in':
        if (!Array.isArray(value) || !value.every(scalar)) throw new TypeError('in requires an array of scalar values');
        break;
      case '>': case '>=': case '<': case '<=':
        if (!numeric(value) && typeof value !== 'string') throw new TypeError('Range conditions require a number or string');
        break;
      default:
        if (!scalar(value)) throw new TypeError('Conditions require a scalar operand');
    }
    return { field: condition.field, op: condition.op as FilterOp, value };
  }));
};

// UTF-8 preserves Unicode scalar order. Comparing code points avoids allocating
// encoded copies of large strings during repeated residual evaluation.
export const compareStrings = (a: string, b: string): number => {
  let left = 0;
  let right = 0;
  while (left < a.length && right < b.length) {
    const x = a.codePointAt(left)!;
    const y = b.codePointAt(right)!;
    if (x !== y) return x < y ? -1 : 1;
    left += x > 0xffff ? 2 : 1;
    right += y > 0xffff ? 2 : 1;
  }
  return left < a.length ? 1 : right < b.length ? -1 : 0;
};

export const readQueryField = (document: ReplicaDocument, field: string): QueryValue | undefined =>
  field === 'deleted' ? document.deleted === true : own(document, field) ?
    (document as Record<string, QueryValue>)[field] : undefined;

// Input documents have already passed the storage typed-value boundary. Only
// operands are copied here, once per compiled matcher, never document payloads.
export const compileConditions = (conditions: readonly ReplicaCondition[]): ((document: ReplicaDocument | null) => boolean) => {
  const frozen = frozenConditions(conditions);
  return document => frozen.every(({ field, op, value: operand }) => {
    if (!document) return false;
    const value = readQueryField(document, field);
    if (value === undefined) return false;
    switch (op) {
      case '==': return equalValue(value, operand);
      case '!=': return !equalValue(value, operand);
      case 'in': return (operand as QueryValue[]).some(item => equalValue(value, item));
      case 'contains': return Array.isArray(value) && value.some(item => equalValue(item, operand));
      default: {
        let comparison: number;
        if (numeric(value) && numeric(operand)) comparison = value < operand ? -1 : value > operand ? 1 : 0;
        else if (typeof value === 'string' && typeof operand === 'string') comparison = compareStrings(value, operand);
        else return false;
        return op === '>' ? comparison > 0 : op === '>=' ? comparison >= 0 : op === '<' ? comparison < 0 : comparison <= 0;
      }
    }
  });
};

export const matchesConditions = (document: ReplicaDocument | null, conditions: readonly ReplicaCondition[]): boolean =>
  compileConditions(conditions)(document);

export const freezeSourceDefinition = (definition: ReplicaSourceDefinition): FrozenSourceDefinition => {
  if (!object(definition) || Object.keys(definition).some(key => !['collection', 'filters', 'orderBy', 'limit'].includes(key))) {
    throw new TypeError('Invalid source definition');
  }
  if (!string(definition.collection) || definition.collection.length > 1024 ||
      !/^[a-zA-Z0-9_.-]+(?:\/[a-zA-Z0-9_.-]+)*$/.test(definition.collection) ||
      definition.collection.split('/').length % 2 !== 1) throw new TypeError('Invalid collection path');
  const filters = frozenConditions(definition.filters).map(condition => ({ ...condition, value: encodeQueryValue(condition.value) }));
  const orderBy = definition.orderBy === undefined ? [] : definition.orderBy;
  if (!Array.isArray(orderBy)) throw new TypeError('Source order must be an array');
  const seen = new Set<string>();
  const order = orderBy.map(item => {
    if (!object(item) || Object.keys(item).some(key => !['field', 'direction'].includes(key))) throw new TypeError('Invalid source order');
    validateField(item.field);
    if (item.direction !== 'asc' && item.direction !== 'desc') throw new TypeError('Invalid order direction');
    if (seen.has(item.field)) throw new TypeError('Duplicate source order field');
    seen.add(item.field);
    return { field: item.field, direction: item.direction };
  });
  if (!seen.has('id')) order.push({ field: 'id', direction: 'asc' });
  if (definition.limit !== undefined && (!Number.isInteger(definition.limit) || definition.limit < 1 || definition.limit > 1000)) {
    throw new TypeError('Source limit must be between 1 and 1000');
  }
  return deepFreeze({ collection: definition.collection, filters, orderBy: order, limit: definition.limit ?? null });
};

export const definitionHash = (definition: FrozenSourceDefinition): Promise<string> => sha256(canonicalJson(definition));

const metadata: (value: unknown) => asserts value is WireMetadata = (value) => {
  if (!object(value) || Object.keys(value).some(key => !['version', 'createdAt', 'updatedAt'].includes(key))) corruption('Invalid wire metadata');
  for (const [key, item] of Object.entries(value)) {
    if (typeof item !== 'string' || !/^(0|-?[1-9][0-9]*)$/.test(item)) corruption('Invalid metadata integer');
    const number = BigInt(item);
    if (number < (key === 'version' ? 0n : int64Min) || number > int64Max) corruption('Metadata integer is outside int64');
  }
};

export const projectDocument = (collection: string, data: DataRecord | null, member: MemberRecord | null): ReplicaDocument | null => {
  if (!data) return null;
  validateRecord(data);
  if (member) {
    validateRecord(member);
    if (member.logicalId !== data.logicalId) corruption('Member and data logical identities differ');
  }
  if (data.existence === 'absent') return null;
  const fields = member?.metadata ?? {};
  const wire = Object.fromEntries(Object.entries(fields).map(([key, value]) => [key, BigInt(value)]));
  const identity = { id: data.logicalId, collection, ...wire };
  return (data.existence === 'deleted' ? { ...identity, deleted: true } :
    { ...decodeBusinessPayload(data.payload), ...identity }) as ReplicaDocument;
};

const exactFields = (value: Record<string, unknown>, names: string[]) => {
  if (names.some(key => !own(value, key)) || Object.keys(value).some(key => !names.includes(key) && !systems.has(key))) {
    corruption('Record fields do not match its kind');
  }
  if (own(value, '_deleted') && value._deleted !== false) corruption('Replica records cannot use physical tombstones');
  if (own(value, '_rev') && !string(value._rev)) corruption('Invalid native revision');
  if (own(value, '_meta') && (!object(value._meta) || typeof value._meta.lwt !== 'number' || !Number.isFinite(value._meta.lwt))) corruption('Invalid native metadata');
  if (own(value, '_attachments') && (!object(value._attachments) || Object.keys(value._attachments).length !== 0)) corruption('Replica records do not support attachments');
};
const existence = (value: unknown) => value === 'live' || value === 'deleted' || value === 'absent';
const jsonObject = (value: unknown) => {
  if (!object(value)) corruption('Expected a JSON object');
  try { canonicalJson(value); } catch (error) { throw new ReplicaStorageError('ReplicaStorageCorruption', 'Invalid JSON object', { cause: error }); }
};

export const validateRecordEnvelope: (value: unknown) => asserts value is ReplicaRecord = (value) => {
  if (!object(value)) corruption('Replica record must be an object');
  if (value.kind === 'c') {
    exactFields(value, ['key', 'kind', 'checkpoint', 'generation', 'phase', 'bootstrapComplete', 'partialDelivery']);
    if (value.key !== 'c:progress' || !nullableString(value.generation) || !string(value.phase) ||
        typeof value.bootstrapComplete !== 'boolean' || typeof value.partialDelivery !== 'boolean') corruption('Invalid control record');
    jsonObject(value.checkpoint);
    return;
  }
  if (value.kind !== 'd' && value.kind !== 'm') corruption('Unknown replica record kind');
  try { validateLogicalId(value.logicalId); } catch (error) { throw new ReplicaStorageError('ReplicaStorageCorruption', 'Invalid record logical identity', { cause: error }); }
  if (typeof value.key !== 'string' || !new RegExp(`^${value.kind}:[0-9a-f]{64}$`).test(value.key)) corruption('Invalid record key');
  if (value.kind === 'd') {
    exactFields(value, ['key', 'kind', 'logicalId', 'existence', 'payload', 'editToken', 'pin', 'wire']);
    if (!existence(value.existence) || typeof value.payload !== 'string' || !nullableString(value.editToken)) corruption('Invalid data state');
    if (value.existence !== 'live' && value.payload !== '') corruption('Non-live data must have an empty payload');
    metadata(value.wire);
    if (value.pin !== null) {
      if (!object(value.pin) || Object.keys(value.pin).some(key => !['token', 'stage', 'settledRound'].includes(key)) ||
          !string(value.pin.token) || value.pin.token !== value.editToken ||
          !['await-settlement', 'await-source'].includes(value.pin.stage as string) ||
          (own(value.pin, 'settledRound') && !string(value.pin.settledRound))) corruption('Invalid edit pin');
    }
  } else {
    exactFields(value, ['key', 'kind', 'logicalId', 'slots', 'observedExistence', 'metadata']);
    if (value.observedExistence !== null && !existence(value.observedExistence)) corruption('Invalid observed existence');
    if (!Array.isArray(value.slots) || value.slots.length > 2) corruption('Member records allow at most two source generations');
    const seen = new Set<string>();
    for (const slot of value.slots) {
      if (!object(slot) || Object.keys(slot).length !== 2 || !string(slot.generation) || typeof slot.member !== 'boolean' || seen.has(slot.generation)) corruption('Invalid member slot');
      seen.add(slot.generation);
    }
    metadata(value.metadata);
  }
};

export const validateRecord: (value: unknown) => asserts value is ReplicaRecord = (value) => {
  validateRecordEnvelope(value);
  if (value.kind === 'd' && value.existence === 'live') decodeBusinessPayload(value.payload);
};

export const validateRecordIdentity = async (record: ReplicaRecord): Promise<void> => {
  validateRecord(record);
  if (record.kind !== 'c' && record.key !== await recordKey(record.kind, record.logicalId)) corruption('Stored record key does not match its logical identity');
};

export const businessEqual = (a: ReplicaRecord, b: ReplicaRecord): boolean => {
  validateRecord(a);
  validateRecord(b);
  if (a.kind !== b.kind || a.key !== b.key) return false;
  if (a.kind === 'd' && b.kind === 'd') return a.logicalId === b.logicalId && a.existence === b.existence &&
    (a.existence !== 'live' || a.payload === b.payload);
  const content = (record: ReplicaRecord) => Object.fromEntries(Object.entries(record).filter(([key]) => !systems.has(key)));
  return canonicalJson(content(a)) === canonicalJson(content(b));
};

export const validateManifest: (value: unknown) => asserts value is AliasManifest = (value) => {
  if (!object(value)) corruption('Manifest must be an object');
  exactFields(value, ['key', 'formatVersion', 'lifecycleId', 'namespace', 'definition', 'definitionHash', 'boundDatabaseId', 'sourceHash', 'state',
    'activePhysicalEpoch', 'physicalEpochs', 'maintenance', 'activeSourceGeneration', 'stagedSourceGeneration', 'sourceReady',
    'lastCompleteRound', 'partialDelivery', 'dirtyUpstream', 'issues', 'recoveryIntent']);
  if (value.key !== 'manifest' || value.formatVersion !== 1) corruption('Unsupported manifest format');
  if (!string(value.lifecycleId) || !nullableString(value.lastCompleteRound)) corruption('Invalid alias lifecycle or completed round');
  const namespace = value.namespace;
  if (!object(namespace) || Object.keys(namespace).length !== 5 ||
      !['endpoint', 'subject', 'database', 'name', 'alias'].every(key => string(namespace[key]))) corruption('Invalid namespace');
  if (!object(value.definition) || Object.keys(value.definition).length !== 4 || !Array.isArray(value.definition.filters)) corruption('Invalid frozen source definition');
  try {
    const definition = value.definition;
    const normalized = freezeSourceDefinition({
      collection: definition.collection as string,
      filters: (definition.filters as Record<string, unknown>[]).map(condition => ({ field: condition.field as string, op: condition.op as FilterOp, value: decodeQueryValue(condition.value) })),
      orderBy: definition.orderBy as FrozenSourceDefinition['orderBy'],
      ...(definition.limit === null ? {} : { limit: definition.limit as number }),
    });
    if (canonicalJson(normalized) !== canonicalJson(definition)) corruption('Noncanonical frozen source definition');
  } catch (error) {
    if (error instanceof ReplicaStorageError) throw error;
    throw new ReplicaStorageError('ReplicaStorageCorruption', 'Invalid frozen source definition', { cause: error });
  }
  if (typeof value.definitionHash !== 'string' || !/^[0-9a-f]{64}$/.test(value.definitionHash)) corruption('Invalid definition hash');
  if (!nullableString(value.boundDatabaseId) || !nullableString(value.sourceHash) ||
      (value.boundDatabaseId === null) !== (value.sourceHash === null)) corruption('Invalid source binding');
  if (!['creating', 'ready', 'removed'].includes(value.state as string) || !string(value.activePhysicalEpoch) ||
      !Array.isArray(value.physicalEpochs) || value.physicalEpochs.length < 1 || value.physicalEpochs.length > 2 ||
      !value.physicalEpochs.every(string) || new Set(value.physicalEpochs).size !== value.physicalEpochs.length ||
      !value.physicalEpochs.includes(value.activePhysicalEpoch)) corruption('Invalid physical epochs');
  if (!nullableString(value.activeSourceGeneration) || !nullableString(value.stagedSourceGeneration) ||
      (value.stagedSourceGeneration !== null && value.stagedSourceGeneration === value.activeSourceGeneration) ||
      typeof value.sourceReady !== 'boolean' || typeof value.partialDelivery !== 'boolean') corruption('Invalid source generation state');
  if (value.maintenance !== null) {
    const state = value.maintenance;
    if (!object(state) || Object.keys(state).length !== 4 || !string(state.id) || !string(state.oldEpoch) || !string(state.newEpoch) ||
        state.oldEpoch === state.newEpoch || !value.physicalEpochs.includes(state.oldEpoch) || !value.physicalEpochs.includes(state.newEpoch) ||
        !['staging', 'flipped'].includes(state.stage as string) ||
        value.activePhysicalEpoch !== (state.stage === 'staging' ? state.oldEpoch : state.newEpoch)) corruption('Invalid maintenance state');
  }
  if (value.dirtyUpstream !== null) {
    const marker = value.dirtyUpstream;
    if (!object(marker) || Object.keys(marker).length !== 5 || !string(marker.id) ||
        !Number.isSafeInteger(marker.session) || (marker.session as number) < 0 || !string(marker.physicalEpoch) ||
        marker.physicalEpoch !== value.activePhysicalEpoch || typeof marker.mayHaveDispatched !== 'boolean' ||
        !Array.isArray(marker.targets) || marker.targets.length > 200) corruption('Invalid upstream marker');
    const ids = new Set<string>();
    for (const target of marker.targets) {
      if (!object(target) || Object.keys(target).length !== 2 || !string(target.token) || !string(target.logicalId) || ids.has(target.logicalId)) corruption('Invalid upstream target');
      try { validateLogicalId(target.logicalId); } catch { corruption('Invalid upstream target identity'); }
      ids.add(target.logicalId);
    }
  }
  if (!Array.isArray(value.issues) || value.issues.length > 200) corruption('Invalid storage issues');
  const issueIds = new Set<string>();
  for (const issue of value.issues) {
    if (!object(issue) || Object.keys(issue).some(key => !['id', 'logicalId', 'code', 'token'].includes(key)) || !string(issue.id) || !string(issue.code) ||
        (own(issue, 'token') && !nullableString(issue.token)) ||
        !nullableString(issue.logicalId) || issueIds.has(issue.id)) corruption('Invalid storage issue');
    if (issue.logicalId !== null) {
      try { validateLogicalId(issue.logicalId); } catch { corruption('Invalid issue logical identity'); }
    }
    issueIds.add(issue.id);
  }
  if (value.recoveryIntent !== null) {
    const intent = value.recoveryIntent;
    const fields = ['id', 'issueId', 'phaseId', 'physicalEpoch', 'action', 'logicalId', 'protectedToken', 'resultToken', 'current', 'desired'];
    if (!object(intent) || Object.keys(intent).length !== fields.length || fields.some(field => !own(intent, field)) ||
        !string(intent.id) || !string(intent.issueId) || !nullableString(intent.phaseId) || intent.physicalEpoch !== value.activePhysicalEpoch ||
        !['adopt', 'merge'].includes(intent.action as string) ||
        !nullableString(intent.protectedToken) || !string(intent.resultToken)) corruption('Invalid recovery intent');
    try { validateLogicalId(intent.logicalId); } catch { corruption('Invalid recovery target identity'); }
    validateRecord(intent.current);
    validateRecord(intent.desired);
    if (intent.current.kind !== 'd' || intent.desired.kind !== 'd' ||
        intent.current.logicalId !== intent.logicalId || intent.desired.logicalId !== intent.logicalId ||
        intent.current.key !== intent.desired.key || intent.desired.editToken !== intent.resultToken) {
      corruption('Recovery records do not match the target identity and result token');
    }
  }
};

export const validateManifestIdentity = async (manifest: AliasManifest): Promise<void> => {
  validateManifest(manifest);
  if (manifest.definitionHash !== await definitionHash(manifest.definition)) corruption('Stored source definition hash does not match its definition');
  if (manifest.recoveryIntent !== null) {
    await validateRecordIdentity(manifest.recoveryIntent.current);
    await validateRecordIdentity(manifest.recoveryIntent.desired);
  }
};
