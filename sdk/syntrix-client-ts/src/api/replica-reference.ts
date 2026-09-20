import type { FilterOp, QueryOrder } from './types.js';
import { decodeQueryValue, encodeQueryValue, type QueryValue } from './value.js';
import type { OpenReplicaOptions, ReplicaSource } from './replica-types.js';
import { freezeSourceDefinition, frozenConditions, validateField } from '../internal/replica/records.js';

export type ReplicaSourceSnapshot = {
  collection: string;
  filters: { field: string; op: FilterOp; value: QueryValue }[];
  orderBy: QueryOrder[];
  limit?: number;
};
export type ReplicaOptionsSnapshot = Omit<OpenReplicaOptions, 'collections'> & {
  collections: Record<string, ReplicaSourceSnapshot>;
};
const sources = new WeakMap<object, { owner: object; definition: ReplicaSourceSnapshot }>();
const freeze = <T>(value: T): T => {
  if (typeof value === 'object' && value !== null) {
    Object.values(value).forEach(freeze);
    Object.freeze(value);
  }
  return value;
};
const create = <T>(owner: object, definition: ReplicaSourceSnapshot): ReplicaSource<T> => {
  freeze(definition);
  const source: ReplicaSource<T> = {
    where: (field, op, value) => {
      const [condition] = frozenConditions([{ field, op, value }]);
      return create<T>(owner, { ...definition, filters: [...definition.filters, condition] });
    },
    orderBy: (field, direction = 'asc') => {
      validateField(field);
      if (!['asc', 'desc'].includes(direction) || definition.orderBy.some(order => order.field === field)) {
        throw new TypeError('Source ordering requires unique fields and asc or desc direction');
      }
      return create<T>(owner, { ...definition, orderBy: [...definition.orderBy, { field, direction }] });
    },
    limit: count => {
      if (!Number.isInteger(count) || count < 1 || count > 1000) throw new RangeError('Source limit must be between 1 and 1000');
      return create<T>(owner, { ...definition, limit: count });
    },
  };
  sources.set(source, { owner, definition });
  return Object.freeze(source);
};
export const createReplicaSource = <T>(owner: object, collection: string): ReplicaSource<T> => {
  freezeSourceDefinition({ collection, filters: [] });
  return create<T>(owner, { collection, filters: [], orderBy: [] });
};

const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' &&
  !Array.isArray(value) && [Object.prototype, null].includes(Object.getPrototypeOf(value));
const namespace = (value: unknown, label: string): string => {
  if (typeof value !== 'string' || !value.trim() || value.includes('\0')) throw new TypeError(`Replica ${label} must be nonempty`);
  encodeQueryValue(value);
  return value;
};
const positiveOptions = <T extends object>(value: T | undefined, fields: readonly string[], label: string): T | undefined => {
  if (value === undefined) return undefined;
  if (!record(value) || Object.keys(value).some(key => !fields.includes(key))) throw new TypeError(`Invalid replica ${label} options`);
  for (const item of Object.values(value)) if (!Number.isSafeInteger(item) || (item as number) <= 0) throw new RangeError(`Replica ${label} values must be positive safe integers`);
  return { ...value };
};
export const snapshotReplicaOptions = (owner: object, options: OpenReplicaOptions): ReplicaOptionsSnapshot => {
  if (!record(options) || Object.keys(options).some(key => !['name', 'collections', 'storageLimits', 'queryLimits', 'sync', 'onDiagnostic'].includes(key))) {
    throw new TypeError('Invalid replica options');
  }
  const name = namespace(options.name, 'name');
  if (!record(options.collections)) throw new TypeError('Replica collections must be an alias map');
  const collections = Object.fromEntries(Object.entries(options.collections).map(([alias, source]) => {
    namespace(alias, 'alias');
    const entry = typeof source === 'object' && source !== null ? sources.get(source) : undefined;
    if (!entry || entry.owner !== owner) throw new TypeError('Replica source must be created by this client');
    const canonical = freezeSourceDefinition(entry.definition);
    const definition: ReplicaSourceSnapshot = {
      collection: canonical.collection,
      filters: canonical.filters.map(filter => ({ ...filter, value: decodeQueryValue(filter.value) })),
      orderBy: canonical.orderBy.map(order => ({ ...order })),
      ...(canonical.limit === null ? {} : { limit: canonical.limit }),
    };
    return [alias, definition];
  }));
  const storageLimits = positiveOptions(options.storageLimits,
    ['maxRecordBytes', 'maxManifestBytes', 'maxMetadataBytes', 'maxKnownIds', 'maxStoredBytes'], 'storage limits');
  if (storageLimits && (storageLimits.maxMetadataBytes ?? 17 * 1024 * 1024) <= (storageLimits.maxRecordBytes ?? 16 * 1024 * 1024)) {
    throw new RangeError('Metadata budget must exceed record budget');
  }
  const queryLimits = positiveOptions(options.queryLimits,
    ['readBytes', 'payloadBytes', 'keyBytes', 'nodes', 'scanCandidates', 'scanBytes', 'queuedKeys', 'queuedBytes', 'outputBytes', 'rebuildAttempts'], 'query limits');
  const sync = positiveOptions(options.sync, ['pollIntervalMs', 'hintDelayMs', 'retryBaseMs', 'retryMaxMs', 'maintenanceBackoffMs'], 'sync');
  if (sync && (sync.retryBaseMs ?? 1000) > (sync.retryMaxMs ?? 30_000)) throw new RangeError('Retry base exceeds maximum');
  if (options.onDiagnostic !== undefined && typeof options.onDiagnostic !== 'function') throw new TypeError('onDiagnostic must be a function');
  return freeze({ name, collections, ...(storageLimits ? { storageLimits } : {}), ...(queryLimits ? { queryLimits } : {}),
    ...(sync ? { sync } : {}), ...(options.onDiagnostic ? { onDiagnostic: options.onDiagnostic } : {}) });
};
