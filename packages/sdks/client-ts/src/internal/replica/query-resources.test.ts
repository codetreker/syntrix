import { describe, expect, test } from 'bun:test';
import { encodeQueryValue } from '../../api/value.js';
import { ReplicaStorageError } from './storage-types.js';
import { deepFreezeDocument, defaultQueryLimits, QueryBudgetExceeded, QueryResources, typedDocumentBytes, validateQueryLimits } from './query-resources.js';

describe('query resources', () => {
  test('reservations enforce shared exact boundaries and release once', () => {
    const resources = new QueryResources({ ...defaultQueryLimits, payloadBytes: 10 });
    const first = resources.reserve('payloadBytes', 4);
    const second = resources.reserve('payloadBytes', 6);
    const snapshot = resources.snapshot;
    expect(snapshot.payloadBytes).toBe(10);
    expect(Object.isFrozen(snapshot)).toBe(true);
    try {
      resources.reserve('payloadBytes', 1);
      throw new Error('Expected budget exhaustion');
    } catch (error) {
      expect(error).toBeInstanceOf(QueryBudgetExceeded);
      expect(error).toBeInstanceOf(ReplicaStorageError);
      expect((error as QueryBudgetExceeded).code).toBe('QueryBudgetExceeded');
      expect((error as QueryBudgetExceeded).kind).toBe('payloadBytes');
    }
    first(); first();
    expect(resources.snapshot.payloadBytes).toBe(6);
    expect(snapshot.payloadBytes).toBe(10);
    second();
    for (const kind of ['keyBytes', 'nodes', 'queuedKeys', 'queuedBytes'] as const) {
      const release = resources.reserve(kind, defaultQueryLimits[kind]);
      expect(() => resources.reserve(kind, 1)).toThrow(QueryBudgetExceeded);
      release(); release();
      expect(resources.snapshot[kind]).toBe(0);
    }
    expect(resources.snapshot.payloadBytes).toBe(0);
    resources.reserve('nodes', 0)();
  });

  test('limits and reservations reject invalid sizes without mutating usage', () => {
    expect(validateQueryLimits()).toEqual(defaultQueryLimits);
    expect(validateQueryLimits({ nodes: 12 }).nodes).toBe(12);
    expect(Object.isFrozen(validateQueryLimits())).toBe(true);
    const resources = new QueryResources();
    for (const value of [NaN, Infinity, -1, 0.5, Number.MAX_SAFE_INTEGER + 1]) {
      expect(() => validateQueryLimits({ nodes: value })).toThrow(TypeError);
      expect(() => resources.reserve('nodes', value)).toThrow(TypeError);
    }
    expect(() => validateQueryLimits({ nodes: 0 })).toThrow(TypeError);
    expect(() => validateQueryLimits({ nodes: undefined })).toThrow(TypeError);
    expect(() => validateQueryLimits({ unexpected: 1 } as Partial<typeof defaultQueryLimits>)).toThrow(TypeError);
    expect(resources.snapshot.nodes).toBe(0);
  });

  test('counts exact typed JSON UTF-8 bytes including escaping and metadata', () => {
    const controls = String.fromCharCode(...Array.from({ length: 32 }, (_, index) => index));
    const shared = { nested: [1n, 1, -0, null] };
    const values = [null, true, false, 0, -0, 1.5, 1e-7, Number.MAX_VALUE, 0n, -(1n << 63n), (1n << 63n) - 1n,
      '', 'ascii"\\/', controls, 'é中文𐀀😀\u2028\u2029', [], {}, [shared, shared],
      { id: '用户', collection: 'users', version: 42n, deleted: false, name: 'Alice', nested: [true, null, { '\t"😀': 'é' }] },
      Object.assign(Object.create(null), { constructor: 1n, __proto__: 2n }),
      'x'.repeat(2 * 1024 * 1024)];
    for (const value of values) {
      expect(typedDocumentBytes(value)).toBe(new TextEncoder().encode(JSON.stringify(encodeQueryValue(value))).byteLength);
    }
  });

  test('rejects the same unsupported inputs as the typed codec', () => {
    const cycle: Record<string, unknown> = {};
    cycle.self = cycle;
    const values = [undefined, Symbol(), () => {}, NaN, Infinity, -(1n << 63n) - 1n, 1n << 63n,
      '\ud800', '\udc00', '\ud800a', { '\ud800': 1 }, new Date(), new Uint8Array(1),
      { [Symbol()]: 1 }, { nested: undefined }, Array(1), cycle];
    for (const value of values) {
      expect(() => encodeQueryValue(value)).toThrow();
      expect(() => typedDocumentBytes(value)).toThrow();
    }
  });

  test('freezes every shared descendant without changing identity or looping on cycles', () => {
    const child = { name: 'Alice' };
    const value: { first: typeof child; array: unknown[]; self?: unknown } = { first: child, array: [child, 1n] };
    value.self = value;
    expect(deepFreezeDocument(value)).toBe(value);
    expect(Object.isFrozen(value)).toBe(true);
    expect(Object.isFrozen(child)).toBe(true);
    expect(Object.isFrozen(value.array)).toBe(true);
    expect(() => { child.name = 'Bob'; }).toThrow();
  });
});
