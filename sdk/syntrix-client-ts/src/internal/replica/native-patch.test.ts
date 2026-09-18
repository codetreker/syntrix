import { describe, expect, test } from 'bun:test';
import { createRequire } from 'node:module';
import {
  cancelRxStorageReplication,
  defaultConflictHandler,
  defaultHashSha256,
  fillWithDefaultSettings,
  getRxReplicationMetaInstanceSchema,
  replicateRxStorageInstance,
  type RxStorageInstanceReplicationState,
} from 'rxdb';
import { getRxStorageMemory } from 'rxdb/plugins/storage-memory';
import { Subject } from 'rxjs';

type Row = { id: string; value: number };
type State = RxStorageInstanceReplicationState<Row>;
const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 1));
const until = async (condition: () => boolean) => {
  const deadline = Date.now() + 3000;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error('Native replication did not reach the expected state');
    await tick();
  }
};
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
};

const fixture = async (replicate = replicateRxStorageInstance, cancel = cancelRxStorageReplication) => {
  const schema = fillWithDefaultSettings<Row>({
    version: 0, primaryKey: 'id', type: 'object',
    properties: { id: { type: 'string', maxLength: 100 }, value: { type: 'number' } },
    required: ['id', 'value'],
  });
  const storage = getRxStorageMemory();
  const options = {
    databaseName: `native-${crypto.randomUUID()}`, databaseInstanceToken: 'native-test',
    multiInstance: false, devMode: false, options: {},
  };
  const fork = await storage.createStorageInstance({ ...options, collectionName: 'fork', schema });
  const meta = await storage.createStorageInstance({
    ...options, collectionName: 'meta', schema: getRxReplicationMetaInstanceSchema(schema, false),
  });
  const wrappedFork = Object.create(fork) as typeof fork;
  const wrappedMeta = Object.create(meta) as typeof meta;
  const stream = new Subject<any>();
  const states: State[] = [];
  const errors: { error: unknown; canceled: boolean }[] = [];
  const checkpoints: { direction: string; data: any }[] = [];
  const writeMeta = meta.bulkWrite.bind(meta);
  wrappedMeta.bulkWrite = async (rows, context) => {
    const result = await writeMeta(rows, context);
    if (context === 'replication-set-checkpoint' && result.error.length === 0) {
      checkpoints.push({ direction: rows[0]!.document.itemId, data: rows[0]!.document.checkpointData });
    }
    return result;
  };
  const handler = {
    masterChangeStream$: stream,
    masterChangesSince: async (checkpoint: any, _batchSize: number): Promise<any> => ({ documents: [], checkpoint }),
    masterWrite: async (_rows: any[]): Promise<any[]> => [],
  };
  const start = (extra: Record<string, unknown> = {}) => {
    const state = replicate<Row>({
      identifier: 'native-test', hashFunction: defaultHashSha256,
      forkInstance: wrappedFork, metaInstance: wrappedMeta,
      pullBatchSize: 1, pushBatchSize: 1,
      conflictHandler: defaultConflictHandler, skipStoringPullMeta: false,
      replicationHandler: handler,
      ...extra,
    });
    state.events.error.subscribe((error) => errors.push({ error, canceled: state.events.canceled.getValue() }));
    states.push(state);
    return state;
  };
  const drain = async (state: State) => {
    for (;;) {
      const queues = [state.streamQueue.down, state.streamQueue.up, state.checkpointQueue];
      await Promise.allSettled(queues);
      await tick();
      if (queues[0] === state.streamQueue.down && queues[1] === state.streamQueue.up && queues[2] === state.checkpointQueue) return;
    }
  };
  const stop = async (state: State) => {
    state.events.canceled.next(true);
    await drain(state);
    // A failed checkpoint remains rejected: cancellation is not a successful flush.
    await Promise.allSettled([cancel(state)]);
  };
  const close = async () => {
    for (const state of states) await stop(state);
    stream.complete();
    await fork.close();
    await meta.close();
  };
  const seed = async (value = 1) => {
    const previous = (await fork.findDocumentsById(['local'], true))[0];
    const result = await fork.bulkWrite([{
      previous,
      document: {
        id: 'local', value, _deleted: false, _attachments: {},
        _meta: { lwt: Date.now() + value }, _rev: `${value}-local`,
      },
    }], 'test-local');
    expect(result.error).toHaveLength(0);
  };
  return { fork, meta, wrappedFork, wrappedMeta, stream, handler, start, drain, stop, close, seed, errors, checkpoints };
};

const returnedFailure = (rows: any[], status = 500) => ({
  error: [{ status, documentId: rows[0].document.id, writeRow: rows[0] }],
}) as any;

describe('patched native replication protocol', () => {
  test('the CommonJS entry also cancels before reporting returned metadata failures', async () => {
    const cjs = createRequire(import.meta.url)('rxdb') as typeof import('rxdb');
    const f = await fixture(cjs.replicateRxStorageInstance, cjs.cancelRxStorageReplication);
    f.handler.masterChangesSince = async () => ({ documents: [{ id: 'remote', value: 1, _deleted: false }], checkpoint: { sequence: 1 } });
    const write = f.wrappedMeta.bulkWrite.bind(f.wrappedMeta);
    f.wrappedMeta.bulkWrite = async (rows, context) => context === 'replication-down-write-meta'
      ? returnedFailure(rows)
      : write(rows, context);
    try {
      const state = f.start();
      await until(() => state.events.canceled.getValue());
      await f.drain(state);
      expect(f.errors).toHaveLength(1);
      expect(f.errors[0]!.canceled).toBe(true);
      expect(f.checkpoints.filter((checkpoint) => checkpoint.direction === 'down')).toHaveLength(0);
    } finally { await f.close(); }
  });

  for (const fault of ['document', 'metadata', 'checkpoint'] as const) {
    test(`downstream ${fault} failure preserves the durable prefix and replays on a fresh state`, async () => {
      const f = await fixture();
      let fetched = 0;
      let failure = true;
      const calls: number[] = [];
      f.handler.masterChangesSince = async (checkpoint) => {
        const next = (checkpoint?.sequence ?? 0) + 1;
        calls.push(next);
        fetched = next;
        return { documents: next <= 3 ? [{ id: `remote-${next}`, value: next, _deleted: false }] : [], checkpoint: { sequence: next } };
      };
      const writeFork = f.wrappedFork.bulkWrite.bind(f.wrappedFork);
      f.wrappedFork.bulkWrite = async (rows, context) => {
        if (fault === 'document' && failure && fetched === 2) {
          failure = false;
          return returnedFailure(rows);
        }
        return writeFork(rows, context);
      };
      const writeMeta = f.wrappedMeta.bulkWrite.bind(f.wrappedMeta);
      f.wrappedMeta.bulkWrite = async (rows, context) => {
        const match = fault === 'metadata' ? context === 'replication-down-write-meta'
          : fault === 'checkpoint' && context === 'replication-set-checkpoint' && rows[0]?.document.itemId === 'down';
        if (match && failure && fetched === 2) {
          failure = false;
          return returnedFailure(rows);
        }
        return writeMeta(rows, context);
      };
      try {
        const state = f.start();
        await until(() => state.events.canceled.getValue());
        await f.drain(state);
        expect(f.errors).toHaveLength(1);
        expect(f.errors[0]!.canceled).toBe(true);
        expect(calls).toEqual([1, 2]);
        expect(f.checkpoints.filter((c) => c.direction === 'down').map((c) => c.data.sequence)).toEqual([1]);
        expect(state.firstSyncDone.down.getValue()).toBe(false);
        await f.stop(state);
        const resumed = f.start();
        await until(() => resumed.firstSyncDone.down.getValue());
        await f.drain(resumed);
        expect(calls).toEqual([1, 2, 2, 3, 4]);
        expect((await f.fork.findDocumentsById(['remote-1', 'remote-2', 'remote-3'], false))).toHaveLength(3);
        expect(f.errors).toHaveLength(1);
      } finally { await f.close(); }
    });
  }

  test('the next pull page waits for document, metadata and checkpoint persistence', async () => {
    const f = await fixture();
    const gates = [deferred(), deferred(), deferred()];
    const contexts: string[] = [];
    let calls = 0;
    f.handler.masterChangesSince = async (checkpoint) => {
      calls++;
      return { documents: calls === 1 ? [{ id: 'remote', value: 1, _deleted: false }] : [], checkpoint: { sequence: 1 } };
    };
    const forkWrite = f.wrappedFork.bulkWrite.bind(f.wrappedFork);
    f.wrappedFork.bulkWrite = async (rows, context) => {
      contexts.push('document'); await gates[0]!.promise;
      return forkWrite(rows, context);
    };
    const metaWrite = f.wrappedMeta.bulkWrite.bind(f.wrappedMeta);
    f.wrappedMeta.bulkWrite = async (rows, context) => {
      if (context === 'replication-down-write-meta') { contexts.push('metadata'); await gates[1]!.promise; }
      if (context === 'replication-set-checkpoint' && rows[0]?.document.itemId === 'down') { contexts.push('checkpoint'); await gates[2]!.promise; }
      return metaWrite(rows, context);
    };
    try {
      const state = f.start();
      for (const [index, context] of ['document', 'metadata', 'checkpoint'].entries()) {
        await until(() => contexts.includes(context));
        expect(calls).toBe(1);
        expect(state.firstSyncDone.down.getValue()).toBe(false);
        gates[index]!.resolve();
      }
      await until(() => state.firstSyncDone.down.getValue());
      expect(calls).toBe(2);
      expect(f.errors).toHaveLength(0);
    } finally { gates.forEach((gate) => gate.resolve()); await f.close(); }
  });

  for (const fault of ['replication-up-write-meta', 'replication-up-write-conflict-meta', 'replication-up-write-conflict', 'replication-set-checkpoint']) {
    test(`upstream ${fault} failure cancels before diagnosis and preserves newer edits on restart`, async () => {
      const f = await fixture();
      await f.seed();
      const pushed: number[] = [];
      let failed = false;
      let conflict = fault.includes('conflict');
      f.handler.masterWrite = async (rows) => {
        pushed.push(...rows.map((row) => row.newDocumentState.value));
        if (conflict) { conflict = false; return rows.map((row) => ({ ...row.newDocumentState, value: 2 })); }
        return [];
      };
      const metaWrite = f.wrappedMeta.bulkWrite.bind(f.wrappedMeta);
      f.wrappedMeta.bulkWrite = async (rows, context) => {
        if (!failed && context === fault) { failed = true; return returnedFailure(rows, 409); }
        return metaWrite(rows, context);
      };
      const forkWrite = f.wrappedFork.bulkWrite.bind(f.wrappedFork);
      f.wrappedFork.bulkWrite = async (rows, context) => {
        if (!failed && context === fault) { failed = true; return returnedFailure(rows); }
        return forkWrite(rows, context);
      };
      // Checkpoint 409 is a valid retry, so use a non-conflict storage error for this path.
      if (fault === 'replication-set-checkpoint') f.wrappedMeta.bulkWrite = async (rows, context) => {
        if (!failed && context === fault) { failed = true; return returnedFailure(rows); }
        return metaWrite(rows, context);
      };
      try {
        const state = f.start();
        await until(() => state.events.canceled.getValue());
        await f.drain(state);
        expect(f.errors).toHaveLength(1);
        expect(f.errors[0]!.canceled).toBe(true);
        expect(f.checkpoints.filter((c) => c.direction === 'up')).toHaveLength(0);
        expect(state.firstSyncDone.up.getValue()).toBe(false);
        await f.stop(state);
        await f.seed(3);
        const resumed = f.start();
        await until(() => resumed.firstSyncDone.up.getValue());
        await f.drain(resumed);
        expect(pushed[pushed.length - 1]).toBe(3);
        expect(f.checkpoints.some((c) => c.direction === 'up')).toBe(true);
        expect(f.errors).toHaveLength(1);
      } finally { await f.close(); }
    });
  }

  for (const path of ['downstream-origin', 'unchanged-business-data']) {
    test(`upstream ${path} early return awaits and owns checkpoint failure`, async () => {
      const f = await fixture();
      await f.seed();
      let pushes = 0;
      f.handler.masterWrite = async () => { pushes++; return []; };
      try {
        const state = f.start();
        await until(() => state.firstSyncDone.up.getValue() && state.firstSyncDone.down.getValue());
        await f.drain(state);
        expect(pushes).toBe(1);
        const previous = (await f.fork.findDocumentsById(['local'], false))[0]!;
        const error = new Error('early-return-checkpoint');
        const metaWrite = f.wrappedMeta.bulkWrite.bind(f.wrappedMeta);
        f.wrappedMeta.bulkWrite = async (rows, context) => {
          if (context === 'replication-set-checkpoint' && rows[0]?.document.itemId === 'up') throw error;
          return metaWrite(rows, context);
        };
        const context = path === 'downstream-origin' ? await state.downstreamBulkWriteFlag : 'local-noop';
        await f.fork.bulkWrite([{
          previous,
          document: { ...previous, _rev: '2-same-value', _meta: { lwt: previous._meta.lwt + 1 } },
        }], context);
        await until(() => state.events.canceled.getValue());
        await f.drain(state);
        expect(pushes).toBe(1);
        expect(f.errors).toEqual([{ error, canceled: true }]);
      } finally { await f.close(); }
    });
  }

  test('cancel during initial checkpoint reads prevents late startup subscriptions', async () => {
    const f = await fixture();
    const gate = deferred();
    let reads = 0;
    const read = f.meta.findDocumentsById.bind(f.meta);
    f.wrappedMeta.findDocumentsById = async (...args) => {
      reads++;
      await gate.promise;
      return read(...args);
    };
    try {
      const state = f.start({ initialCheckpoint: { downstream: { sequence: 0 }, upstream: { sequence: 0 } } });
      await until(() => reads === 2);
      await cancelRxStorageReplication(state);
      gate.resolve();
      await tick();
      await tick();
      await f.drain(state);
      f.stream.next('RESYNC');
      expect(f.stream.observers).toHaveLength(0);
      expect(f.errors).toHaveLength(0);
      expect(f.checkpoints).toHaveLength(0);
    } finally { gate.resolve(); await f.close(); }
  });

  for (const fault of ['initial-read', 'pull-handler', 'push-handler', 'live-stream', 'up-scan', 'wait-before-persist', 'sync-wait-before-persist', 'hash']) {
    test(`${fault} rejection is owned and reported once after cancellation`, async () => {
      const f = await fixture();
      const error = new Error(fault);
      const extra: Record<string, unknown> = {};
      if (fault === 'initial-read') {
        extra.initialCheckpoint = { downstream: { sequence: 0 }, upstream: { sequence: 0 } };
        f.wrappedMeta.findDocumentsById = async () => { throw error; };
      }
      if (fault === 'pull-handler') f.handler.masterChangesSince = async () => { throw error; };
      if (fault === 'push-handler') { await f.seed(); f.handler.masterWrite = async () => { throw error; }; }
      if (fault === 'up-scan') f.wrappedFork.getChangedDocumentsSince = async () => { throw error; };
      if (fault === 'wait-before-persist') extra.waitBeforePersist = async () => { throw error; };
      if (fault === 'sync-wait-before-persist') extra.waitBeforePersist = () => { throw error; };
      if (fault === 'hash') extra.hashFunction = async () => { throw error; };
      try {
        const state = f.start(extra);
        if (fault === 'live-stream' || fault.includes('wait-before-persist')) {
          await until(() => state.firstSyncDone.up.getValue() && state.firstSyncDone.down.getValue());
          if (fault === 'live-stream') f.stream.error(error);
          else await f.seed();
        }
        await until(() => state.events.canceled.getValue());
        await f.drain(state);
        expect(f.errors).toEqual([{ error, canceled: true }]);
      } finally { await f.close(); }
    });
  }
});
