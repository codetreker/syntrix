import { ReplicaStorageError } from './storage-types.js';

export type QueryLimits = {
  readBytes: number;
  payloadBytes: number;
  keyBytes: number;
  nodes: number;
  scanCandidates: number;
  scanBytes: number;
  queuedKeys: number;
  queuedBytes: number;
  outputBytes: number;
  rebuildAttempts: number;
};

export const defaultQueryLimits: Readonly<QueryLimits> = Object.freeze({
  readBytes: 64 * 1024 * 1024,
  payloadBytes: 128 * 1024 * 1024,
  keyBytes: 64 * 1024 * 1024,
  nodes: 1_000_000,
  scanCandidates: 100_000,
  scanBytes: 128 * 1024 * 1024,
  queuedKeys: 100_000,
  queuedBytes: 16 * 1024 * 1024,
  outputBytes: 16 * 1024 * 1024,
  rebuildAttempts: 8,
});

export const validateQueryLimits = (options: Partial<QueryLimits> = {}): Readonly<QueryLimits> => {
  const limits = { ...defaultQueryLimits, ...options };
  for (const [kind, value] of Object.entries(limits)) {
    if (!Object.prototype.hasOwnProperty.call(defaultQueryLimits, kind) || !Number.isSafeInteger(value) || value <= 0) {
      throw new TypeError(`Query limit ${kind} must be a positive safe integer`);
    }
  }
  return Object.freeze(limits);
};

export class QueryBudgetExceeded extends ReplicaStorageError {
  constructor(readonly kind: keyof QueryLimits) {
    super('QueryBudgetExceeded', `Query exceeded its ${kind} budget`);
    this.name = 'QueryBudgetExceeded';
  }
}

export type QueryResourceKind = 'payloadBytes' | 'keyBytes' | 'nodes' | 'queuedKeys' | 'queuedBytes';
export type QueryResourceSnapshot = Readonly<Record<QueryResourceKind, number>>;

export class QueryResources {
  readonly limits: Readonly<QueryLimits>;
  private readonly counters: Record<QueryResourceKind, number> = {
    payloadBytes: 0, keyBytes: 0, nodes: 0, queuedKeys: 0, queuedBytes: 0,
  };

  constructor(limits: QueryLimits = defaultQueryLimits) {
    this.limits = validateQueryLimits(limits);
  }

  get snapshot(): QueryResourceSnapshot {
    return Object.freeze({ ...this.counters });
  }

  reserve(kind: QueryResourceKind, amount: number): () => void {
    if (!Number.isSafeInteger(amount) || amount < 0) {
      throw new TypeError('Query reservation must be a nonnegative safe integer');
    }
    if (amount > this.limits[kind] - this.counters[kind]) throw new QueryBudgetExceeded(kind);
    this.counters[kind] += amount;
    let released = false;
    return () => {
      if (released) return;
      released = true;
      this.counters[kind] -= amount;
    };
  }
}

const minInt64 = -(1n << 63n);
const maxInt64 = (1n << 63n) - 1n;

// Count JSON escaping and UTF-8 directly so admission never creates a second
// large string or the expanded typed-value tree that it is trying to bound.
const jsonStringBytes = (value: string): number => {
  let bytes = 2;
  for (let index = 0; index < value.length; index++) {
    const unit = value.charCodeAt(index);
    if (unit === 0x22 || unit === 0x5c || unit === 8 || unit === 9 || unit === 10 || unit === 12 || unit === 13) {
      bytes += 2;
    } else if (unit < 0x20) {
      bytes += 6;
    } else if (unit < 0x80) {
      bytes++;
    } else if (unit < 0x800) {
      bytes += 2;
    } else if (unit >= 0xd800 && unit <= 0xdbff) {
      const next = value.charCodeAt(++index);
      if (!(next >= 0xdc00 && next <= 0xdfff)) throw new TypeError('Query string contains an unpaired surrogate');
      bytes += 4;
    } else if (unit >= 0xdc00 && unit <= 0xdfff) {
      throw new TypeError('Query string contains an unpaired surrogate');
    } else {
      bytes += 3;
    }
  }
  return bytes;
};

export const typedDocumentBytes = (value: unknown): number => {
  const ancestors = new Set<object>();
  const count = (item: unknown): number => {
    if (item === null) return '{"type":"null"}'.length;
    switch (typeof item) {
      case 'boolean': return '{"type":"bool","value":}'.length + (item ? 4 : 5);
      case 'string': return '{"type":"string","value":}'.length + jsonStringBytes(item);
      case 'bigint': {
        if (item < minInt64 || item > maxInt64) throw new RangeError('Query integer is outside the int64 range');
        return '{"type":"int64","value":""}'.length + item.toString().length;
      }
      case 'number': {
        if (!Number.isFinite(item)) throw new TypeError('Query numbers must be finite');
        return '{"type":"float64","value":}'.length + JSON.stringify(item).length;
      }
      case 'object': {
        if (ancestors.has(item)) throw new TypeError('Query values must not contain cycles');
        ancestors.add(item);
        try {
          if (Array.isArray(item)) {
            let bytes = '{"type":"array","value":[]}'.length;
            for (let index = 0; index < item.length; index++) bytes += count(item[index]) + (index ? 1 : 0);
            return bytes;
          }
          const prototype = Object.getPrototypeOf(item);
          if (prototype !== Object.prototype && prototype !== null) throw new TypeError('Query objects must be plain objects');
          if (Object.getOwnPropertySymbols(item).length !== 0) throw new TypeError('Query objects must have string keys');
          let bytes = '{"type":"object","value":{}}'.length;
          let comma = 0;
          for (const key in item) {
            if (!Object.prototype.hasOwnProperty.call(item, key)) continue;
            bytes += comma + jsonStringBytes(key) + 1 + count((item as Record<string, unknown>)[key]);
            comma = 1;
          }
          return bytes;
        } finally {
          ancestors.delete(item);
        }
      }
      default: throw new TypeError(`Unsupported query value type: ${typeof item}`);
    }
  };
  return count(value);
};

export const deepFreezeDocument = <T>(value: T): T => {
  const visited = new WeakSet<object>();
  const freeze = (item: unknown): void => {
    if (typeof item !== 'object' || item === null || visited.has(item)) return;
    visited.add(item);
    for (const key in item) {
      if (Object.prototype.hasOwnProperty.call(item, key)) freeze((item as Record<string, unknown>)[key]);
    }
    Object.freeze(item);
  };
  freeze(value);
  return value;
};
