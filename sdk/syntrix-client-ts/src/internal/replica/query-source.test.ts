import { describe, expect, test } from 'bun:test';
import { defaultHashSha256 } from 'rxdb';
import { ReadBudget } from './backend.js';
import { canonicalJson } from './records.js';
import { createVisibilityHashCache } from './query-source.js';

type VisibilityManifest = Parameters<ReturnType<typeof createVisibilityHashCache>>[0];
const manifest = (patch: Partial<VisibilityManifest> = {}): VisibilityManifest => ({
  _rev: '1-a', activePhysicalEpoch: 'epoch', activeSourceGeneration: 'generation',
  dirtyUpstream: null, issues: [], ...patch,
});

describe('query visibility fingerprint', () => {
  test('hashes the sorted union of protections independently from ordering, duplicates and metadata', async () => {
    const derive = createVisibilityHashCache(); const budget = new ReadBudget(64 * 1024 * 1024);
    const first = manifest({ dirtyUpstream: { id: 'phase1', physicalEpoch: 'epoch', session: 1, mayHaveDispatched: false,
      targets: [{ logicalId: 'z', token: 'token1' }, { logicalId: 'a', token: 'token2' }] },
      issues: [{ id: 'issue1', logicalId: 'z', code: 'conflict' }, { id: 'global', logicalId: null, code: 'failure' }],
    });
    const value = await derive(first, budget);
    expect(value).toBe(await defaultHashSha256(canonicalJson(['epoch', 'generation', ['a', 'z']])));
    const second = manifest({ _rev: '2-b', dirtyUpstream: { ...first.dirtyUpstream!, id: 'phase2', mayHaveDispatched: true,
      targets: [{ logicalId: 'z', token: 'token3' }] },
      issues: [{ id: 'issue2', logicalId: 'a', code: 'other' }, { id: 'issue3', logicalId: 'z', code: 'other' }],
    });
    expect(await derive(second, budget)).toBe(value);
    expect(await derive({ ...second, _rev: '3-c', dirtyUpstream: null, issues: [{ id: 'issue', logicalId: 'a', code: 'conflict' }] }, budget)).not.toBe(value);
    expect(await derive({ ...second, _rev: '4-d', activePhysicalEpoch: 'other-epoch' }, budget)).not.toBe(value);
    expect(await derive({ ...second, _rev: '5-e', activeSourceGeneration: null }, budget)).not.toBe(value);
    expect(budget.usedBytes).toBe(0);
  });

  test('retains only the latest revision hash and reuses it without allocating another fingerprint', async () => {
    const derive = createVisibilityHashCache(); const budget = new ReadBudget(100_000);
    const first = manifest(); const second = manifest({ _rev: '2-b' });
    const value = await derive(first, budget);
    const noSpace = new ReadBudget(1);
    expect(await derive(first, noSpace)).toBe(value);
    expect(noSpace.peakBytes).toBe(0);
    expect(await derive(second, budget)).toBe(value);
    await expect(derive(first, noSpace)).rejects.toMatchObject({ code: 'ReplicaReadBudgetExceeded' });
    expect(noSpace.usedBytes).toBe(0);
    expect(await derive(second, noSpace)).toBe(value);
  });

  test('counts Unicode and JSON escapes before materialization and releases reservations on failure', async () => {
    const logicalId = 'é中😀"\\\t\u0001'.repeat(4000);
    const input = manifest({ issues: [{ id: 'issue', logicalId, code: 'conflict' }] });
    const derive = createVisibilityHashCache();
    const rejected = new ReadBudget(100_000);
    await expect(derive(input, rejected)).rejects.toMatchObject({ code: 'ReplicaReadBudgetExceeded' });
    expect(rejected.usedBytes).toBe(0);
    // Rejection happens after reference admission but before JSON/hash buffers.
    expect(rejected.peakBytes).toBe(1024 + 128);
    const accepted = new ReadBudget(4 * 1024 * 1024);
    expect(await derive(input, accepted)).toBe(await defaultHashSha256(canonicalJson(['epoch', 'generation', [logicalId]])));
    expect(accepted.peakBytes).toBeGreaterThan(100_000);
    expect(accepted.peakBytes).toBeLessThanOrEqual(accepted.maxBytes);
    expect(accepted.usedBytes).toBe(0);
  });

  test('hash failure is not cached and releases both temporary reservations', async () => {
    const derive = createVisibilityHashCache(); const budget = new ReadBudget(100_000);
    const malformed = manifest({ issues: [{ id: 'issue', logicalId: '\ud800', code: 'conflict' }] });
    await expect(derive(malformed, budget)).rejects.toThrow();
    expect(budget.usedBytes).toBe(0);
    expect(await derive(manifest(), budget)).toBe(await defaultHashSha256(canonicalJson(['epoch', 'generation', []])));
  });
});
