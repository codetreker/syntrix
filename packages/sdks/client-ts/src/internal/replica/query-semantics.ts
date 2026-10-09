import { decodeQueryValue, encodeQueryValue, type QueryValue } from '../../api/value.js';
import { canonicalJson, compileConditions, equalValue, frozenConditions, readQueryField, validateField, validateLogicalId } from './records.js';
import type { ReplicaCondition, ReplicaDocument } from './storage-types.js';

export type ReplicaQueryOrder = Readonly<{ field: string; direction: 'asc' | 'desc' }>;
export type ReplicaQuerySpec = {
  filters?: readonly ReplicaCondition[];
  orderBy?: readonly ReplicaQueryOrder[];
  limit?: number;
  startAfter?: string;
  showDeleted?: boolean;
};
export type ReplicaOrderValue = undefined | null | boolean | number | bigint | Uint8Array;
export type ReplicaOrderKey = Readonly<{ values: readonly ReplicaOrderValue[]; id: string; bytes: number }>;
export type NormalizedReplicaQuery = Readonly<{
  filters: readonly ReplicaCondition[];
  orderBy: readonly ReplicaQueryOrder[];
  limit: number | undefined;
  showDeleted: boolean;
  startAfter: ReplicaOrderKey | null;
  canonicalKey: string;
  cursorScope: string;
}>;
const encoder = new TextEncoder();
const decoder = new TextDecoder('utf-8', { fatal: true });
const cursorLimit = 16 * 1024;
const plain = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' &&
  !Array.isArray(value) && (Object.getPrototypeOf(value) === Object.prototype || Object.getPrototypeOf(value) === null);
const exact = (value: Record<string, unknown>, fields: string[]) =>
  Object.keys(value).length === fields.length && fields.every(field => Object.prototype.hasOwnProperty.call(value, field));
const canonicalOperand = (value: QueryValue): QueryValue => {
  if (typeof value === 'number' && Number.isInteger(value)) {
    const integer = BigInt(value);
    if (integer >= -(1n << 63n) && integer < 1n << 63n) return integer;
  }
  return value;
};
const operandIdentity = (value: QueryValue): string => canonicalJson(encodeQueryValue(canonicalOperand(value)));

// Reserve before normalization copies operands and builds canonical strings.
// The factor covers typed-node JSON escaping, UTF-8 digest input and retained
// UTF-16 strings; it is a logical budget, not a JavaScript heap measurement.
export const estimateQuerySpecBytes = (spec: ReplicaQuerySpec): number => {
  if (!plain(spec)) throw new TypeError('Invalid replica query');
  let bytes = 4096;
  const valueBytes = (value: unknown): number => {
    if (typeof value === 'string') return 256 + value.length * 64;
    if (value === null || ['number', 'bigint', 'boolean', 'undefined'].includes(typeof value)) return 256;
    throw new TypeError('Query operands must be scalars');
  };
  if (spec.filters !== undefined && !Array.isArray(spec.filters)) throw new TypeError('Query filters must be an array');
  for (const filter of spec.filters ?? []) {
    if (!plain(filter)) throw new TypeError('Invalid query filter');
    bytes += valueBytes(filter.field) + 512;
    if (Array.isArray(filter.value)) for (const value of filter.value) bytes += valueBytes(value);
    else bytes += valueBytes(filter.value);
  }
  if (spec.orderBy !== undefined && !Array.isArray(spec.orderBy)) throw new TypeError('Query order must be an array');
  for (const order of spec.orderBy ?? []) {
    if (!plain(order)) throw new TypeError('Invalid query order');
    bytes += valueBytes(order.field) + 256;
  }
  bytes += valueBytes(spec.startAfter);
  return bytes;
};

export const createQueryMatcher = compileConditions;

export const normalizeReplicaQuery = async (spec: ReplicaQuerySpec, scope: string, mode: 'get' | 'watch' = 'get'): Promise<NormalizedReplicaQuery> => {
  if (!plain(spec) || Object.keys(spec).some(key => !['filters', 'orderBy', 'limit', 'startAfter', 'showDeleted'].includes(key))) {
    throw new TypeError('Invalid replica query');
  }
  if (typeof scope !== 'string' || scope.length === 0) throw new TypeError('Query scope is required');
  encodeQueryValue(scope);
  if (spec.showDeleted !== undefined && typeof spec.showDeleted !== 'boolean') throw new TypeError('showDeleted must be boolean');
  if (spec.limit !== undefined && (!Number.isInteger(spec.limit) || spec.limit < 1 || spec.limit > 1000)) {
    throw new TypeError('Query limit must be between 1 and 1000');
  }
  if (spec.startAfter !== undefined && (mode === 'watch' || typeof spec.startAfter !== 'string')) {
    throw new TypeError('startAfter requires a page query and string cursor');
  }
  const conditions = frozenConditions(spec.filters === undefined ? [] : spec.filters);
  const unique = new Map<string, ReplicaCondition>();
  for (const condition of conditions) {
    let value = canonicalOperand(condition.value);
    if (condition.op === 'in') {
      const values: QueryValue[] = [];
      for (const item of condition.value as QueryValue[]) {
        if (!values.some(previous => equalValue(previous, item))) values.push(canonicalOperand(item));
        if (values.length > 256) throw new TypeError('in accepts at most 256 distinct values');
      }
      values.sort((a, b) => { const x = operandIdentity(a); const y = operandIdentity(b); return x < y ? -1 : x > y ? 1 : 0; });
      value = values;
    }
    const normalized = { field: condition.field, op: condition.op, value };
    unique.set(canonicalJson({ ...normalized, value: encodeQueryValue(value) }), normalized);
  }
  const filters = frozenConditions([...unique.entries()].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([, value]) => value));
  const inputOrder = spec.orderBy === undefined ? [] : spec.orderBy;
  if (!Array.isArray(inputOrder)) throw new TypeError('Query order must be an array');
  const seen = new Set<string>();
  const orderBy = inputOrder.map(order => {
    if (!plain(order) || !exact(order, ['field', 'direction'])) throw new TypeError('Invalid query order');
    validateField(order.field);
    if (order.direction !== 'asc' && order.direction !== 'desc') throw new TypeError('Invalid query order direction');
    if (seen.has(order.field)) throw new TypeError('Duplicate query order field');
    seen.add(order.field);
    return Object.freeze({ field: order.field, direction: order.direction });
  });
  if (!seen.has('id')) orderBy.push(Object.freeze({ field: 'id', direction: 'asc' }));
  const showDeleted = spec.showDeleted === true;
  const canonicalKey = canonicalJson({ filters: filters.map(filter => ({ ...filter, value: encodeQueryValue(filter.value) })), orderBy, showDeleted });
  const limit = spec.limit ?? (mode === 'get' ? 100 : undefined);
  const startAfter = spec.startAfter;
  const digest = await crypto.subtle.digest('SHA-256', encoder.encode(canonicalJson({ scope, query: canonicalKey })));
  const cursorScope = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('');
  const normalized = { filters, orderBy: Object.freeze(orderBy), showDeleted,
    limit, canonicalKey, cursorScope,
    startAfter: null as ReplicaOrderKey | null };
  if (startAfter !== undefined) normalized.startAfter = decodeQueryCursor(startAfter, normalized);
  return Object.freeze(normalized);
};

const scalarValue = (value: QueryValue | undefined): Exclude<ReplicaOrderValue, Uint8Array> | string => {
  if (value !== undefined && value !== null && typeof value === 'object') throw new TypeError('Arrays and objects cannot supply query ordering fields');
  if (value !== undefined) encodeQueryValue(value);
  return value as Exclude<ReplicaOrderValue, Uint8Array> | string;
};
export const estimateOrderKeyBytes = (document: ReplicaDocument, order: readonly ReplicaQueryOrder[]): number => {
  validateLogicalId(document.id);
  let bytes = 64 + document.id.length * 2;
  for (const field of order) {
    const value = scalarValue(readQueryField(document, field.field));
    bytes += 24 + (typeof value === 'string' ? value.length * 3 : 8);
  }
  return bytes;
};
export const makeOrderKey = (document: ReplicaDocument, order: readonly ReplicaQueryOrder[]): ReplicaOrderKey => {
  validateLogicalId(document.id);
  let bytes = 64 + document.id.length * 2;
  const values = order.map(field => {
    const value = scalarValue(readQueryField(document, field.field));
    const result = typeof value === 'string' ? encoder.encode(value) : value;
    bytes += 24 + (result instanceof Uint8Array ? result.byteLength : 8);
    return result;
  });
  return Object.freeze({ values: Object.freeze(values), id: document.id, bytes });
};
const rank = (value: ReplicaOrderValue): number => value === undefined ? 0 : value === null ? 1 :
  value === false ? 2 : value === true ? 3 : value instanceof Uint8Array ? 5 : 4;
const compareValue = (a: ReplicaOrderValue, b: ReplicaOrderValue): number => {
  const difference = rank(a) - rank(b);
  if (difference) return Math.sign(difference);
  if (a instanceof Uint8Array && b instanceof Uint8Array) {
    for (let i = 0; i < Math.min(a.length, b.length); i++) if (a[i] !== b[i]) return a[i] < b[i] ? -1 : 1;
    return Math.sign(a.length - b.length);
  }
  if ((typeof a === 'number' || typeof a === 'bigint') && (typeof b === 'number' || typeof b === 'bigint')) {
    return a < b ? -1 : a > b ? 1 : 0;
  }
  return 0;
};
export const compareOrderKeys = (a: ReplicaOrderKey, b: ReplicaOrderKey, order: readonly ReplicaQueryOrder[]): number => {
  for (let index = 0; index < order.length; index++) {
    const comparison = compareValue(a.values[index], b.values[index]);
    if (comparison) return order[index].direction === 'desc' ? -comparison : comparison;
  }
  return 0;
};
const cursorNode = (value: ReplicaOrderValue) => value === undefined ? { type: 'missing' } :
  encodeQueryValue(value instanceof Uint8Array ? decoder.decode(value) : value);
export const encodeQueryCursor = (key: ReplicaOrderKey, query: Pick<NormalizedReplicaQuery, 'cursorScope' | 'orderBy'>): string => {
  if (key.values.length !== query.orderBy.length) throw new TypeError('Invalid cursor order key');
  // Reject oversized source strings before decoding byte keys or constructing
  // typed JSON copies. Final encoded length is checked again for escaping.
  let minimumBytes = key.id.length + query.cursorScope.length;
  for (const value of key.values) {
    minimumBytes += value instanceof Uint8Array ? value.byteLength + 1 : 7;
    if (minimumBytes > cursorLimit) throw new TypeError('Query cursor exceeds 16 KiB');
  }
  const json = canonicalJson({ version: 1, scope: query.cursorScope, id: key.id, values: key.values.map(cursorNode) });
  if (json.length > cursorLimit) throw new TypeError('Query cursor exceeds 16 KiB');
  const bytes = encoder.encode(json);
  if (Math.ceil(bytes.length / 3) * 4 > cursorLimit) throw new TypeError('Query cursor exceeds 16 KiB');
  return btoa(String.fromCharCode(...bytes)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
};
export const decodeQueryCursor = (cursor: string, query: Pick<NormalizedReplicaQuery, 'cursorScope' | 'orderBy'>): ReplicaOrderKey => {
  if (cursor.length > cursorLimit || !/^[A-Za-z0-9_-]+$/.test(cursor)) throw new TypeError('Invalid query cursor');
  let parsed: unknown;
  try {
    const bytes = Uint8Array.from(atob(cursor.replace(/-/g, '+').replace(/_/g, '/')), character => character.charCodeAt(0));
    parsed = JSON.parse(decoder.decode(bytes));
  } catch (cause) { throw new TypeError('Invalid query cursor encoding', { cause }); }
  if (!plain(parsed) || !exact(parsed, ['version', 'scope', 'id', 'values']) || parsed.version !== 1 || parsed.scope !== query.cursorScope ||
      !Array.isArray(parsed.values) || parsed.values.length !== query.orderBy.length) throw new TypeError('Query cursor scope or shape mismatch');
  validateLogicalId(parsed.id);
  let bytes = 64 + parsed.id.length * 2;
  const values = parsed.values.map(value => {
    const decoded = plain(value) && exact(value, ['type']) && value.type === 'missing' ? undefined : decodeQueryValue(value);
    const scalar = scalarValue(decoded);
    const stored = typeof scalar === 'string' ? encoder.encode(scalar) : scalar;
    bytes += 24 + (stored instanceof Uint8Array ? stored.byteLength : 8);
    return stored;
  });
  const key = Object.freeze({ values: Object.freeze(values), id: parsed.id, bytes });
  const idIndex = query.orderBy.findIndex(field => field.field === 'id');
  if (idIndex < 0 || !(values[idIndex] instanceof Uint8Array) || decoder.decode(values[idIndex] as Uint8Array) !== parsed.id) {
    throw new TypeError('Query cursor logical identity mismatch');
  }
  if (encodeQueryCursor(key, query) !== cursor) throw new TypeError('Noncanonical query cursor');
  return key;
};
