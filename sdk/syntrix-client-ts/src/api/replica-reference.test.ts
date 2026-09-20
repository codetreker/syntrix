import { describe, expect, it } from 'bun:test';
import { createReplicaSource, snapshotReplicaOptions } from './replica-reference.js';
import type { OpenReplicaOptions, ReplicaDatabase, ReplicaSource } from './replica-types.js';

describe('replica source definitions', () => {
  it('keeps public references typed without exposing internal replication or child navigation', () => {
    interface Task { title: string; score: bigint; }
    const typecheck = async (database: ReplicaDatabase, source: ReplicaSource<Task>) => {
      const tasks = database.collection<Task>('tasks');
      const created = await tasks.add({ title: 'Review', score: 3n });
      await created.ifMatch('score', '==', 3n).update({ title: 'Done' });
      await created.set({ title: 'Again', score: 4n });
      const document = await created.get({ showDeleted: true });
      if (document && !document.deleted) { const score: bigint = document.score; void score; }
      const query = tasks.where('score', '>=', 2n).orderBy('score', 'desc').limit(20);
      const page = await query.getPage();
      if (page.nextCursor) await query.startAfter(page.nextCursor).get();
      const stop = query.showDeleted().watch(documents => { for (const value of documents) void value.id; });
      stop();
      const inspection = await database.sync.inspect<Task>('tasks', { id: created.id, readCurrent: true });
      if (inspection.issueId && inspection.document) await database.sync.resolve('tasks', { kind: 'adopt-server',
        issueId: inspection.issueId, id: inspection.document.id, editToken: inspection.document.editToken, physicalEpoch: inspection.physicalEpoch });
      await database.sync.pause(); await database.sync.resume('tasks');
      const unsubscribe = database.sync.subscribe(status => { for (const alias of Object.values(status.aliases)) void alias.pending; });
      unsubscribe(); await created.delete(); await database.removeCollection('tasks'); await database.close();
      // @ts-expect-error A remote source definition cannot request an application cursor.
      source.startAfter('cursor');
      // @ts-expect-error Replica references do not navigate uncopied child collections.
      created.collection('children');
      // @ts-expect-error Public mutation retains the declared business type.
      await created.set({ title: 1, score: 2n });
      // @ts-expect-error Manual Push remains private to replication.
      database.push([]);
    };
    expect(typeof typecheck).toBe('function');
  });
  it('builds immutable independent sources and copies exact typed operands', () => {
    const owner = {}, values = [1n, 2];
    const base = createReplicaSource(owner, 'projects/p1/tasks');
    const filtered = base.where('score', 'in', values);
    const window = filtered.orderBy('updatedAt', 'desc').limit(2);
    values[0] = 9n;
    expect(Object.isFrozen(base)).toBe(true);
    const snapshot = snapshotReplicaOptions(owner, { name: 'cache', collections: { all: base, filtered, window } });
    expect(snapshot.collections.all.filters).toEqual([]);
    expect(snapshot.collections.filtered.filters).toEqual([{ field: 'score', op: 'in', value: [1n, 2] }]);
    expect(snapshot.collections.filtered.limit).toBeUndefined();
    expect(snapshot.collections.window.limit).toBe(2);
    expect(snapshot.collections.window.orderBy).toEqual([{ field: 'updatedAt', direction: 'desc' }, { field: 'id', direction: 'asc' }]);
    expect(Object.isFrozen(snapshot.collections.filtered.filters[0].value)).toBe(true);
    expect('startAfter' in base).toBe(false); expect('get' in base).toBe(false); expect('showDeleted' in base).toBe(false);
  });

  it('freezes registration options and supports aliases without prototype collisions', () => {
    const owner = {}, source = createReplicaSource(owner, 'users');
    const options: OpenReplicaOptions = { name: 'cache', collections: Object.fromEntries([['__proto__', source], ['constructor', source]]),
      storageLimits: { maxKnownIds: 10 }, queryLimits: { nodes: 100 }, sync: { pollIntervalMs: 50 } };
    const captured = snapshotReplicaOptions(owner, options);
    options.name = 'changed'; Reflect.set(options.collections, 'constructor', source.limit(9)); options.storageLimits!.maxKnownIds = 20;
    expect(captured.name).toBe('cache'); expect(captured.storageLimits!.maxKnownIds).toBe(10);
    expect(Object.keys(captured.collections)).toEqual(['__proto__', 'constructor']);
    expect(Reflect.get(captured.collections, 'constructor').limit).toBeUndefined();
    expect(Object.getPrototypeOf(captured.collections)).toBe(Object.prototype);
    expect(Object.isFrozen(captured.sync)).toBe(true);
  });

  it('preserves explicitly positioned ID ordering and rejects malformed source operations', () => {
    const owner = {}, base = createReplicaSource(owner, 'users');
    const source = base.orderBy('id', 'desc').orderBy('score');
    expect(snapshotReplicaOptions(owner, { name: 'cache', collections: { source } }).collections.source.orderBy)
      .toEqual([{ field: 'id', direction: 'desc' }, { field: 'score', direction: 'asc' }]);
    for (const path of ['', 'users/alice', 'users//tasks', 'users/*/tasks', '界']) expect(() => createReplicaSource(owner, path)).toThrow();
    for (const count of [0, -1, 1001, 1.1, NaN]) expect(() => base.limit(count)).toThrow();
    expect(() => source.orderBy('id')).toThrow('unique');
    expect(() => base.orderBy('name', 'bad' as 'asc')).toThrow();
    for (const field of ['', 'nested.value', '\0']) expect(() => base.where(field, '==', 1)).toThrow();
    expect(() => base.where('name', 'unknown' as '==', 1)).toThrow();
    expect(() => base.where('name', '==', { nested: true })).toThrow();
    expect(() => base.where('name', 'in', [null, { nested: true }])).toThrow();
    expect(() => base.where('name', '>', null)).toThrow();
    expect(() => base.where('name', '==', undefined as never)).toThrow();
    expect(() => base.where('name', '==', '\ud800')).toThrow();
  });

  it('requires same-client provenance and rejects unsupported open options synchronously', () => {
    const owner = {}, source = createReplicaSource(owner, 'users');
    expect(() => snapshotReplicaOptions({}, { name: 'cache', collections: { source } })).toThrow('this client');
    for (const options of [null, [], {}, { name: 'cache', collections: [] }, { name: ' ', collections: { source } },
      { name: 'cache', collections: { x: {} } }, { name: 'cache', collections: { '\0': source } },
      { name: 'cache', collections: { source }, extra: true },
      { name: 'cache', collections: { source }, storageLimits: { unknown: 1 } },
      { name: 'cache', collections: { source }, queryLimits: { nodes: 0 } },
      { name: 'cache', collections: { source }, sync: { retryBaseMs: 31_000 } },
      { name: 'cache', collections: { source }, storageLimits: { maxMetadataBytes: 1 } },
      { name: 'cache', collections: { source }, onDiagnostic: true },
    ]) expect(() => snapshotReplicaOptions(owner, options as unknown as OpenReplicaOptions)).toThrow();
    const callback = () => undefined;
    expect(snapshotReplicaOptions(owner, { name: 'cache', collections: { source }, onDiagnostic: callback }).onDiagnostic).toBe(callback);
  });

  it('allows a named replica with no configured sources for historical alias removal', () => {
    const snapshot = snapshotReplicaOptions({}, { name: 'cache', collections: {} });
    expect(snapshot).toEqual({ name: 'cache', collections: {} });
    expect(Object.isFrozen(snapshot.collections)).toBe(true);
  });
});
