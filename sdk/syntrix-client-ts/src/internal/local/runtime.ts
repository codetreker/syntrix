import {
  cancelRxStorageReplication,
  getChangedDocumentsSince,
  replicateRxStorageInstance,
  type RxConflictHandler,
  type RxReplicationWriteToMasterRow,
  type RxStorageInstance,
  type RxStorageInstanceReplicationState,
  type RxStorageReplicationMeta,
  type WithDeleted,
  type WithDeletedAndAttachments,
  type HashFunction,
} from 'rxdb';
import { EMPTY, Subject, type Subscription } from 'rxjs';
import { readBoundedChanges, validateBoundedReadOptions, type BoundedReadOptions } from './bounded-reader.js';

export { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
export { defaultConflictHandler, defaultHashSha256, fillWithDefaultSettings, getRxReplicationMetaInstanceSchema } from 'rxdb';

export interface SourcePage<T, C extends object> {
  documents: WithDeletedAndAttachments<T>[];
  checkpoint: C;
  complete: boolean;
}

// RxDB shallow-merges checkpoints. One stable key makes each opaque source
// value replace its predecessor, including fields removed by phase changes.
type NativeSourceCheckpoint<C extends object> = { source: C };

export interface LocalReplicationOptions<T, C extends object> {
  identifier: string;
  forkInstance: RxStorageInstance<T, any, any>;
  metaInstance: RxStorageInstance<RxStorageReplicationMeta<T, any>, any, any>;
  conflictHandler: RxConflictHandler<T>;
  hashFunction: HashFunction;
  pullBatchSize?: number;
  pushBatchSize?: number;
  readBounds?: BoundedReadOptions;
  readSource(checkpoint: C | undefined, limit: number, signal: AbortSignal): Promise<SourcePage<T, C>>;
  writeRemote(rows: RxReplicationWriteToMasterRow<T>[], signal: AbortSignal): Promise<WithDeleted<T>[]>;
  createProgressDocument?(page: SourcePage<T, C>): WithDeletedAndAttachments<T>;
  isControlDocument?(document: WithDeleted<T>): boolean;
  onCheckpoint?(checkpoint: C, status: { complete: boolean }, signal: AbortSignal): Promise<void>;
  onError?(error: unknown): void;
}

export interface LocalReplicationRuntime {
  readonly ready: boolean;
  readonly stopped: boolean;
  readonly error: unknown;
  requestResync(): void;
  waitForIdle(): Promise<void>;
  /** Drains owned work; the caller retains ownership of both storage instances. */
  close(): Promise<void>;
}

export const createLocalReplicationRuntime = <T, C extends object>(
  options: LocalReplicationOptions<T, C>,
): LocalReplicationRuntime => {
  const limits = validateBoundedReadOptions(options.readBounds);
  const pullBatchSize = options.pullBatchSize ?? 201;
  const pushBatchSize = options.pushBatchSize ?? 50;
  for (const count of [pullBatchSize, pushBatchSize]) {
    if (!Number.isSafeInteger(count) || count <= 0) throw new RangeError('Replication batch size must be a positive safe integer');
  }
  if (pushBatchSize > limits.maxDocuments) throw new RangeError('Push batch size exceeds the bounded scan limit');

  const abort = new AbortController();
  const closedError = new Error('Local replication is closed');
  const invalidations = new Subject<'RESYNC'>();
  const pending = new Set<Promise<unknown>>();
  const subscriptions: Subscription[] = [];
  let state: RxStorageInstanceReplicationState<T>;
  let stopped = false;
  let ready = false;
  let failed = false;
  let failure: unknown;
  let dirty = false;
  let sourceDirty = false;
  let continuation = false;
  let sourceIdle = false;
  let scheduled = false;
  let scheduler: Promise<void> = Promise.resolve();
  let wire: Promise<unknown> = Promise.resolve();
  let pageToCommit: SourcePage<T, C> | undefined;
  let closing: Promise<void> | undefined;

  const stop = () => {
    if (stopped) return;
    stopped = true;
    ready = false;
    abort.abort();
    state?.events.canceled.next(true);
    subscriptions.forEach(subscription => subscription.unsubscribe());
    invalidations.complete();
  };
  const fail = (error: unknown) => {
    if (stopped) return;
    failed = true;
    failure = error;
    stop();
    try {
      options.onError?.(error);
    } catch (observerError) {
      failure = Object.assign(new Error('Local replication error observer failed'), { cause: error, observerError });
    }
  };
  const assertOpen = () => {
    if (stopped) throw failed ? failure : closedError;
  };
  const track = <R>(operation: () => Promise<R>): Promise<R> => {
    const task = (async () => {
      assertOpen();
      return operation();
    })();
    pending.add(task);
    void task.then(() => pending.delete(task), error => {
      pending.delete(task);
      if (stopped) {
        if (!failed && error !== closedError && error !== abort.signal.reason) {
          failed = true;
          failure = error;
        }
      } else {
        fail(error);
      }
    });
    return task;
  };

  // Explicit composition prevents the native protocol from unwrapping our
  // admission and scan boundaries through underlyingPersistentStorage.
  const decorate = <D>(storage: RxStorageInstance<D, any, any>, fork: boolean): RxStorageInstance<D, any, any> => ({
    databaseName: storage.databaseName,
    collectionName: storage.collectionName,
    schema: storage.schema,
    internals: storage.internals,
    options: storage.options,
    bulkWrite: (rows, context) => track(async () => {
      const result = await storage.bulkWrite(rows, context);
      if (fork) {
        const controlConflict = result.error.find(error => error.status === 409 && options.isControlDocument?.(error.writeRow.document as unknown as WithDeleted<T>));
        if (controlConflict) throw controlConflict;
      }
      return result;
    }),
    findDocumentsById: (ids, deleted) => track(() => storage.findDocumentsById(ids, deleted)),
    query: query => track(() => storage.query(query)),
    count: query => track(() => storage.count(query)),
    getAttachmentData: (id, attachment, digest) => track(() => storage.getAttachmentData(id, attachment, digest)),
    changeStream: () => fork ? EMPTY : storage.changeStream(),
    cleanup: minimumAge => track(() => storage.cleanup(minimumAge)),
    close: () => track(() => storage.close()),
    remove: () => track(() => storage.remove()),
    getChangedDocumentsSince: (limit, checkpoint) => track(async () => {
      if (fork && !ready) return { documents: [], checkpoint };
      return readBoundedChanges(
        (count, cursor) => track(() => getChangedDocumentsSince(storage, count, cursor)),
        checkpoint,
        limit,
        limits,
      );
    }),
  });

  const commitPage = async () => {
    if (!pageToCommit) return;
    assertOpen();
    const page = pageToCommit;
    // The caller reaches this barrier only after the native persistence chain,
    // including its checkpoint, has settled. The hook may activate a generation.
    if (options.onCheckpoint) await track(() => options.onCheckpoint!(page.checkpoint, { complete: page.complete }, abort.signal));
    assertOpen();
    pageToCommit = undefined;
    if (page.complete) {
      continuation = false;
      sourceIdle = true;
      if (!ready) {
        ready = true;
        dirty = true;
      }
    } else {
      continuation = true;
    }
  };

  const schedule = () => {
    if (scheduled || stopped) return;
    scheduled = true;
    scheduler = Promise.resolve().then(async () => {
      if (stopped || !state || state.events.active.up.value || state.events.active.down.value) return;
      await Promise.all([state.streamQueue.down, state.streamQueue.up, state.checkpointQueue]);
      if (stopped || state.events.active.up.value || state.events.active.down.value) return;
      await commitPage();
      if (stopped || (!dirty && !sourceDirty && !continuation)) return;
      if (sourceDirty || continuation) sourceIdle = false;
      sourceDirty = false;
      dirty = false;
      invalidations.next('RESYNC');
    }).catch(fail).then(() => {
      scheduled = false;
      if (!stopped && !state.events.active.up.value && !state.events.active.down.value && (dirty || sourceDirty || continuation || pageToCommit)) schedule();
    });
  };

  state = replicateRxStorageInstance({
    identifier: options.identifier,
    hashFunction: input => track(() => options.hashFunction(input)),
    forkInstance: decorate(options.forkInstance, true),
    metaInstance: decorate(options.metaInstance, false),
    conflictHandler: options.conflictHandler,
    pullBatchSize,
    pushBatchSize,
    skipStoringPullMeta: false,
    replicationHandler: {
      masterChangeStream$: invalidations,
      masterChangesSince: (nativeCheckpoint: NativeSourceCheckpoint<C> | undefined, limit) => track(async () => {
        await commitPage();
        if (sourceIdle) return { documents: [], checkpoint: nativeCheckpoint };
        continuation = false;
        const checkpoint = nativeCheckpoint === undefined ? undefined : nativeCheckpoint.source;
        const page = await options.readSource(checkpoint, limit, abort.signal);
        assertOpen();
        if (typeof page.complete !== 'boolean' || !page.checkpoint || typeof page.checkpoint !== 'object') throw new TypeError('Source page requires an object checkpoint and completion flag');
        let documents = page.documents;
        if (documents.length === 0) {
          const unchangedTerminal = page.complete && checkpoint !== undefined && JSON.stringify(checkpoint) === JSON.stringify(page.checkpoint);
          if (!unchangedTerminal) {
            if (!options.createProgressDocument || !options.isControlDocument) throw new Error('Empty source progress requires an identifiable progress document');
            const progress = options.createProgressDocument(page);
            if (!options.isControlDocument(progress)) throw new Error('Progress document must be classified as control');
            documents = [progress];
          }
        }
        if (documents.length > limit) throw new RangeError('Source page exceeded its requested limit');
        if (new TextEncoder().encode(JSON.stringify(documents)).byteLength > limits.maxDocumentBytes) throw new RangeError('Source page exceeds its encoded byte limit');
        pageToCommit = page;
        return { documents, checkpoint: { source: page.checkpoint } };
      }),
      masterWrite: rows => {
        const previous = wire;
        const task = track(async () => {
          await previous;
          assertOpen();
          if (!ready) throw new Error('Upstream write attempted before source readiness');
          const business = rows.filter(row => !options.isControlDocument?.(row.newDocumentState));
          if (business.length === 0) return [];
          const conflicts = await options.writeRemote(business, abort.signal);
          assertOpen();
          return conflicts;
        });
        wire = task;
        return task;
      },
    },
  });
  subscriptions.push(state.events.error.subscribe(fail));
  subscriptions.push(state.events.active.up.subscribe(schedule), state.events.active.down.subscribe(schedule));
  subscriptions.push(options.forkInstance.changeStream().subscribe({
    next: event => {
      if (event.context.startsWith('replication-downstream-')) return;
      if (event.events.some(change => !options.isControlDocument?.(change.documentData))) {
        dirty = true;
        schedule();
      }
    },
    error: fail,
  }));

  const settle = async () => {
    while (true) {
      const queues = [state.streamQueue.up, state.streamQueue.down, state.checkpointQueue, scheduler, wire];
      const work = [...pending];
      await Promise.allSettled([...queues, ...work]);
      await Promise.resolve();
      if (pending.size === 0 && queues[0] === state.streamQueue.up && queues[1] === state.streamQueue.down && queues[2] === state.checkpointQueue && queues[3] === scheduler && queues[4] === wire) return;
    }
  };

  return {
    get ready() { return ready; },
    get stopped() { return stopped; },
    get error() { return failure; },
    requestResync: () => {
      assertOpen();
      sourceDirty = true;
      schedule();
    },
    waitForIdle: async () => {
      await settle();
      if (failed) throw failure;
    },
    close: () => {
      if (!closing) {
        stop();
        closing = (async () => {
          await settle();
          try {
            await cancelRxStorageReplication(state);
          } catch (error) {
            if (error !== closedError) throw error;
          }
          if (failed) throw failure;
        })();
      }
      return closing;
    },
  };
};
