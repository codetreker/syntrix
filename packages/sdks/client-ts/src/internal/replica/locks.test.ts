import { expect, test } from 'bun:test';
import { createAliasLocks, type AliasLockOwner } from './locks.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(r => { resolve = r; }); return { promise, resolve }; };
test('alias exclusive queues behind shared owners and view write serializes projections', async () => {
  const manager = createTestLockManager();
  const a = createAliasLocks('a'.repeat(64), manager), b = createAliasLocks('a'.repeat(64), manager);
  const entered = deferred(), release = deferred();
  const order: string[] = [];
  const first = a.withAlias('shared', owner => a.withView(owner, 'exclusive', async () => {
    order.push('write'); entered.resolve(); await release.promise;
  }));
  await entered.promise;
  const second = b.withAlias('shared', owner => b.withView(owner, 'shared', async () => { order.push('read'); }));
  const maintenance = a.withAlias('exclusive', owner => a.withView(owner, 'exclusive', async () => { order.push('maintenance'); }));
  await Promise.resolve(); expect(order).toEqual(['write']);
  release.resolve(); await Promise.all([first, second, maintenance]);
  expect(order).toEqual(['write', 'read', 'maintenance']);
});
test('owners cannot be forged, reused, transferred or nested; queued locks abort', async () => {
  const locks = createAliasLocks('b'.repeat(64), createTestLockManager());
  let saved!: AliasLockOwner;
  await locks.withAlias('exclusive', async owner => {
    saved = owner;
    await locks.withView(owner, 'exclusive', async view => {
      locks.assertViewOwner(view, 'exclusive');
      await expect(locks.withView(owner, 'shared', async () => {})).rejects.toThrow('Nested');
    });
  });
  expect(() => locks.assertAliasOwner(saved)).toThrow();
  expect(() => locks.assertAliasOwner({ mode: 'exclusive' })).toThrow();
  const entered = deferred(), release = deferred();
  const active = locks.withAlias('exclusive', async () => { entered.resolve(); await release.promise; });
  await entered.promise;
  const controller = new AbortController();
  const queued = locks.withAlias('shared', async () => { throw new Error('must not enter'); }, controller.signal);
  controller.abort(new Error('stopped'));
  await expect(queued).rejects.toThrow('stopped');
  release.resolve(); await active;
});
