import { describe, expect, test } from 'bun:test';
import {
  compareOrderKeys, createQueryMatcher, decodeQueryCursor, encodeQueryCursor, estimateOrderKeyBytes,
  estimateQuerySpecBytes, makeOrderKey, normalizeReplicaQuery, type ReplicaQuerySpec,
} from './query-semantics.js';
import type { ReplicaDocument } from './storage-types.js';
const doc = (id: string, value: Record<string, unknown> = {}): ReplicaDocument => ({ id, collection: 'items', ...value }) as ReplicaDocument;
const scope = 'endpoint/subject/database/replica/alias';

describe('replica exact query semantics', () => {
  test('compiles immutable predicates with exact families and immediate array members', async () => {
    const spec: ReplicaQuerySpec = { filters: [{ field: 'score', op: 'in', value: [1n, 1, 9007199254740993n] }] };
    const normalized = await normalizeReplicaQuery(spec, scope);
    (spec.filters![0].value as unknown[]).push(4);
    const match = createQueryMatcher(normalized.filters);
    expect(match(doc('a', { score: 1 }))).toBe(true);
    expect(match(doc('a', { score: 9007199254740993n }))).toBe(true);
    expect(match(doc('a', { score: 9007199254740992 }))).toBe(false);
    expect(match(doc('a', { score: 4 }))).toBe(false);
    expect(match(null)).toBe(false);
    const condition = (op: '==' | '!=' | 'in' | 'contains' | '>', value: any) => createQueryMatcher([{ field: 'x', op, value }]);
    for (const [op, operand] of [['==', null], ['!=', null], ['in', [null]], ['contains', null], ['>', 0]] as const) {
      expect(condition(op, operand)(doc('a'))).toBe(false);
    }
    expect(condition('!=', null)(doc('a', { x: [] }))).toBe(true);
    expect(condition('==', null)(doc('a', { x: null }))).toBe(true);
    expect(condition('contains', 1n)(doc('a', { x: [[], { x: 1 }, 1] }))).toBe(true);
    expect(condition('contains', 1)(doc('a', { x: [[1], { x: 1 }] }))).toBe(false);
    expect(condition('>', 1)(doc('a', { x: '2' }))).toBe(false);
    expect(condition('>', '\ue000')(doc('a', { x: '\u{10000}' }))).toBe(true);
    expect(createQueryMatcher([{ field: 'deleted', op: '==', value: false }])(doc('a'))).toBe(true);
    expect(createQueryMatcher([{ field: 'deleted', op: '==', value: false }])(doc('a', { deleted: true }))).toBe(false);
    expect(Object.isFrozen(normalized.filters[0].value)).toBe(true);
  });

  test('canonicalizes AND order, duplicate predicates, numeric equality and in sets', async () => {
    const a = await normalizeReplicaQuery({ filters: [
      { field: 'x', op: 'in', value: [1n, 1, -0, null, 'x'] }, { field: 'y', op: '==', value: 2 },
    ] }, scope);
    const b = await normalizeReplicaQuery({ limit: 3, filters: [
      { field: 'y', op: '==', value: 2n }, { field: 'x', op: 'in', value: ['x', null, 0n, 1] },
      { field: 'y', op: '==', value: 2 },
    ] }, scope);
    expect(a.canonicalKey).toBe(b.canonicalKey);
    expect(a.cursorScope).toBe(b.cursorScope);
    expect(a.cursorScope).toMatch(/^[a-f0-9]{64}$/);
    expect(a.limit).toBe(100);
    expect((await normalizeReplicaQuery({}, scope, 'watch')).limit).toBeUndefined();
    expect(a.orderBy).toEqual([{ field: 'id', direction: 'asc' }]);
    const values = Array.from({ length: 256 }, (_, index) => index);
    await expect(normalizeReplicaQuery({ filters: [{ field: 'x', op: 'in', value: [...values, 1n] }] }, scope)).resolves.toBeDefined();
    await expect(normalizeReplicaQuery({ filters: [{ field: 'x', op: 'in', value: [...values, 256] }] }, scope)).rejects.toThrow('256');
    expect(createQueryMatcher((await normalizeReplicaQuery({ filters: [{ field: 'x', op: 'in', value: [] }] }, scope)).filters)(doc('a', { x: 1 }))).toBe(false);
  });

  test('orders missing/null/bool/exact numerics/UTF8 with logical ID ties', async () => {
    const query = await normalizeReplicaQuery({ orderBy: [{ field: 'x', direction: 'asc' }] }, scope);
    const rows = [doc('10', { x: '\u{10000}' }), doc('09', { x: '\ue000' }), doc('08', { x: 9007199254740993n }),
      doc('07', { x: 9007199254740992 }), doc('06b', { x: 1n }), doc('06a', { x: 1 }), doc('05', { x: 0.5 }),
      doc('04', { x: true }), doc('03', { x: false }), doc('02', { x: null }), doc('01')];
    const sorted = rows.map(row => makeOrderKey(row, query.orderBy)).sort((a, b) => compareOrderKeys(a, b, query.orderBy));
    expect(sorted.map(key => key.id)).toEqual(['01', '02', '03', '04', '05', '06a', '06b', '07', '08', '09', '10']);
    for (const row of rows) expect(estimateOrderKeyBytes(row, query.orderBy)).toBeGreaterThanOrEqual(makeOrderKey(row, query.orderBy).bytes);
    const descending = await normalizeReplicaQuery({ orderBy: [{ field: 'id', direction: 'desc' }] }, scope);
    expect(descending.orderBy).toHaveLength(1);
    expect(compareOrderKeys(makeOrderKey(doc('b'), descending.orderBy), makeOrderKey(doc('a'), descending.orderBy), descending.orderBy)).toBeLessThan(0);
    for (const value of [[], {}, NaN, Infinity, '\ud800']) {
      expect(() => estimateOrderKeyBytes(doc('a', { x: value }), query.orderBy)).toThrow();
      expect(() => makeOrderKey(doc('a', { x: value }), query.orderBy)).toThrow();
    }
    const source = doc('a', { x: 'large'.repeat(1000) });
    const key = makeOrderKey(source, query.orderBy);
    expect(key.values[0]).toBeInstanceOf(Uint8Array);
    expect('document' in key).toBe(false);
  });

  test('cursor is typed, scope-bound, limit-independent and strictly validated', async () => {
    const spec: ReplicaQuerySpec = { orderBy: [{ field: 'x', direction: 'asc' }, { field: 'missing', direction: 'desc' }] };
    const query = await normalizeReplicaQuery(spec, scope);
    const key = makeOrderKey(doc('alice', { x: 9007199254740993n }), query.orderBy);
    const cursor = encodeQueryCursor(key, query);
    const continuation = await normalizeReplicaQuery({ ...spec, startAfter: cursor, limit: 2 }, scope);
    expect(continuation.startAfter).toEqual(key);
    expect(decodeQueryCursor(cursor, query)).toEqual(key);
    for (const changed of [{ ...spec, showDeleted: true }, { filters: [{ field: 'x', op: '==', value: 1 }] }, { orderBy: [] }]) {
      await expect(normalizeReplicaQuery({ ...changed, startAfter: cursor } as ReplicaQuerySpec, scope)).rejects.toThrow();
    }
    await expect(normalizeReplicaQuery({ ...spec, startAfter: cursor }, scope + '/other')).rejects.toThrow();
    await expect(normalizeReplicaQuery({ ...spec, startAfter: cursor }, scope, 'watch')).rejects.toThrow();
    for (const invalid of ['', '!', cursor + '=', 'a'.repeat(16385), btoa('null'), btoa('{}')]) {
      expect(() => decodeQueryCursor(invalid, query)).toThrow();
    }
    const mutate = (change: (node: any) => void) => {
      const node = JSON.parse(atob(cursor.replace(/-/g, '+').replace(/_/g, '/')));
      change(node);
      return btoa(JSON.stringify(node)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
    };
    for (const change of [
      (node: any) => { node.extra = true; }, (node: any) => { node.version = 2; },
      (node: any) => { node.id = 'bob'; }, (node: any) => { node.values.pop(); },
      (node: any) => { node.values[0] = { type: 'object', value: {} }; },
      (node: any) => { node.values[0] = { type: 'int64', value: '01' }; },
    ]) expect(() => decodeQueryCursor(mutate(change), query)).toThrow();
    const longQuery = await normalizeReplicaQuery({ filters: [{ field: 'x', op: '==', value: 'v'.repeat(20000) }] }, scope);
    expect(encodeQueryCursor(makeOrderKey(doc('a'), longQuery.orderBy), longQuery).length).toBeLessThan(1024);
    expect(() => encodeQueryCursor(makeOrderKey(doc('a', { x: 'v'.repeat(20000) }), query.orderBy), query)).toThrow('16 KiB');
  });

  test('captures caller configuration before asynchronous scope hashing', async () => {
    const mutable: ReplicaQuerySpec = { limit: 1, filters: [{ field: 'x', op: '==', value: 1 }] };
    const pending = normalizeReplicaQuery(mutable, scope);
    mutable.limit = 1_000_000;
    mutable.startAfter = 'invalid';
    mutable.showDeleted = true;
    mutable.filters![0].value = 2;
    mutable.orderBy = [{ field: 'x', direction: 'desc' }];
    const query = await pending;
    expect(query.limit).toBe(1);
    expect(query.startAfter).toBeNull();
    expect(query.showDeleted).toBe(false);
    expect(query.orderBy).toEqual([{ field: 'id', direction: 'asc' }]);
    expect(createQueryMatcher(query.filters)(doc('a', { x: 1 }))).toBe(true);
    const cursorQuery = await normalizeReplicaQuery({}, scope);
    const cursor = encodeQueryCursor(makeOrderKey(doc('a'), cursorQuery.orderBy), cursorQuery);
    const watched: ReplicaQuerySpec = {};
    const opening = normalizeReplicaQuery(watched, scope, 'watch');
    watched.startAfter = cursor;
    watched.limit = 1_000_000;
    const ready = await opening;
    expect(ready.startAfter).toBeNull();
    expect(ready.limit).toBeUndefined();
  });

  test('rejects malformed configuration and estimates inputs before normalization', async () => {
    const bad: unknown[] = [null, [], { filters: null }, { orderBy: null }, { extra: true }, { limit: 0 }, { limit: 1001 }, { limit: 1.5 },
      { showDeleted: 1 }, { startAfter: 1 }, { filters: [{ field: 'a.b', op: '==', value: 1 }] },
      { orderBy: {} }, { orderBy: [{ field: 'x', direction: 'invalid' }] },
      { orderBy: [{ field: 'x', direction: 'asc' }, { field: 'x', direction: 'desc' }] },
      { orderBy: [{ field: 'x', direction: 'asc', extra: true }] }];
    for (const spec of bad) await expect(normalizeReplicaQuery(spec as ReplicaQuerySpec, scope)).rejects.toThrow();
    await expect(normalizeReplicaQuery({}, '')).rejects.toThrow();
    expect(estimateQuerySpecBytes({ filters: [{ field: 'x', op: 'in', value: ['x'.repeat(1000), 1n] }] })).toBeGreaterThan(64000);
    for (const invalid of [null, { filters: {} }, { filters: [null] }, { filters: [{ value: {} }] }, { orderBy: {} }, { orderBy: [null] }]) {
      expect(() => estimateQuerySpecBytes(invalid as ReplicaQuerySpec)).toThrow();
    }
  });
});
