import type { Subscription } from 'rxjs';
import { ReadBudget } from './backend.js';
import type { AliasStorage } from './storage.js';
import type { AliasQueryAccess, QueryProjection, QueryView } from './query-source.js';
import { QueryViewChangedError } from './query-source.js';
import { ReplicaStorageError, type ReplicaDocument } from './storage-types.js';
import { compareOrderKeys, createQueryMatcher, encodeQueryCursor, estimateOrderKeyBytes, estimateQuerySpecBytes, makeOrderKey, normalizeReplicaQuery,
  type NormalizedReplicaQuery, type ReplicaOrderKey, type ReplicaQueryOrder, type ReplicaQuerySpec } from './query-semantics.js';
import { OrderedQueryTree } from './query-tree.js';
import { QueryBudgetExceeded, QueryResources, validateQueryLimits, typedDocumentBytes, deepFreezeDocument, type QueryLimits } from './query-resources.js';

export type ReplicaQueryActivity = Readonly<{
  phase: 'contended' | 'recovered'; operationId: string; startedAt: number;
  code: 'ReplicaQueryViewContention' | 'ReplicaQueryWorkContention';
}>;
export type ReplicaQueryPage = { documents: ReplicaDocument[]; nextCursor: string | null; effectiveOrder: readonly ReplicaQueryOrder[] };
export type ReplicaQueryClient = {
  get(spec?: ReplicaQuerySpec): Promise<ReplicaDocument[]>;
  getPage(spec?: ReplicaQuerySpec): Promise<ReplicaQueryPage>;
  watch(spec: ReplicaQuerySpec, onResult: (documents: ReplicaDocument[]) => void, onError?: (error: unknown) => void): () => void;
  close(): Promise<void>;
  debugStats(): ReturnType<Manager['stats']>;
};
type CachedRow = { document: ReplicaDocument; bytes: number; refs: number; release(): void; };
type Candidate = { row: CachedRow; key: ReplicaOrderKey; physicalKey: string; releaseKey(): void; releaseNode(): void; };
type Index = { tree: OrderedQueryTree<ReplicaOrderKey, Candidate>; byKey: Map<string, Candidate> };
type Observer = { client: Client; query: NormalizedReplicaQuery; result(page: ReplicaQueryPage): void; error(error: unknown): void;
  releaseCursor(): void; releaseLast(): void; wantsCursor: boolean; once: boolean; last: readonly CachedRow[] | undefined; closed: boolean; };
type Client = { manager: Manager; access: AliasQueryAccess; observers: Set<Observer>; pending: Set<() => void>; preparations: Set<Promise<void>>; closed: boolean; abort(): void; onActivity?: (event: ReplicaQueryActivity) => void; };
class QueryContention extends Error {
  constructor(readonly kind: 'rebuildAttempts' | 'scanCandidates' | 'scanBytes') { super('Replica query requires another bounded work round'); }
}
const managers = new Map<string, Manager>();
const sameView = (a: QueryView | undefined, b: QueryView) => a?.physicalEpoch === b.physicalEpoch && a.sourceGeneration === b.sourceGeneration && a.visibilityHash === b.visibilityHash;
const closedError = () => new ReplicaStorageError('ReplicaQueryClosed', 'Replica query client is closed');
// Filter equality deliberately unifies numeric values; result equality must
// preserve their public types so bigint/number edits remain observable.
const equalResult = (left: unknown, right: unknown): boolean => {
  if (left === right) return true;
  if (typeof left !== typeof right || left === null || right === null || typeof left !== 'object') return false;
  if (Array.isArray(left)) return Array.isArray(right) && left.length === right.length && left.every((value, index) => equalResult(value, right[index]));
  if (Array.isArray(right)) return false;
  const a = left as Record<string, unknown>; const b = right as Record<string, unknown>;
  const keys = Object.keys(a);
  return keys.length === Object.keys(b).length && keys.every(key => Object.prototype.hasOwnProperty.call(b, key) && equalResult(a[key], b[key]));
};
const pause = () => new Promise<void>(resolve => setTimeout(resolve, 0));
const releaseIndex = (index: Index) => {
  for (const candidate of index.byKey.values()) { candidate.row.release(); candidate.releaseKey(); candidate.releaseNode(); }
  index.tree.clear(); index.byKey.clear();
};

class Manager {
  readonly resources: QueryResources;
  readonly budget: ReadBudget;
  readonly clients = new Set<Client>();
  readonly states = new Map<string, QueryState>();
  readonly cache = new Map<string, CachedRow>();
  private tail: Promise<void> = Promise.resolve();
  private jobs = 0;
  private timer: ReturnType<typeof setInterval> | undefined;
  private visibility = () => { if (typeof document === 'undefined' || document.visibilityState === 'visible') this.checkViews(); };
  constructor(readonly namespace: string, readonly limits: Readonly<QueryLimits>) {
    this.resources = new QueryResources(limits); this.budget = new ReadBudget(limits.readBytes);
  }
  enqueue(operation: () => Promise<void>): Promise<void> {
    this.jobs++;
    const result = this.tail.then(async () => { try { await operation(); } finally { this.jobs--; this.activity(); } });
    this.tail = result.catch(() => undefined); return result;
  }
  stats() { return { ...this.resources.snapshot, queries: this.states.size, cacheEntries: this.cache.size, readPeakBytes: this.budget.peakBytes }; }
  access(namespace: string): AliasQueryAccess {
    for (const client of this.clients) if (!client.closed && !client.access.signal.aborted && client.access.namespace === namespace) return client.access;
    throw closedError();
  }
  activity() {
    if (this.states.size && !this.timer) {
      this.timer = setInterval(() => this.checkViews(), 10_000);
      if (typeof document !== 'undefined') document.addEventListener('visibilitychange', this.visibility);
    } else if (!this.states.size && this.timer) {
      clearInterval(this.timer); this.timer = undefined;
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', this.visibility);
    }
    if (!this.clients.size && !this.states.size && this.jobs === 0 && managers.get(this.namespace) === this) managers.delete(this.namespace);
  }
  checkViews() { for (const state of this.states.values()) state.schedule(); }
  row(namespace: string, view: QueryView, projection: QueryProjection): CachedRow | null {
    if (!projection.visible) return null;
    const identityBytes = 64 + 2 * (namespace.length + view.physicalEpoch.length + view.visibilityHash.length + projection.key.length + projection.revision.length);
    const releaseIdentity = this.resources.reserve('keyBytes', identityBytes);
    const identity = `${namespace}:${view.physicalEpoch}:${view.visibilityHash}:${projection.key}:${projection.revision}`;
    const existing = this.cache.get(identity);
    if (existing) { releaseIdentity(); existing.refs++; return existing; }
    let release: (() => void) | undefined;
    try {
      release = this.resources.reserve('payloadBytes', projection.encodedBytes * 8 + 256);
      const document = projection.decode();
      if (!document) { release(); releaseIdentity(); return null; }
      deepFreezeDocument(document);
      const row: CachedRow = { document, bytes: typedDocumentBytes(document), refs: 1, release: () => {
        row.refs--;
        if (row.refs === 0) { this.cache.delete(identity); release!(); releaseIdentity(); }
      } };
      this.cache.set(identity, row); return row;
    } catch (error) { release?.(); releaseIdentity(); throw error; }
  }
}

class QueryState {
  readonly observers = new Set<Observer>();
  readonly pending = new Map<string, () => void>();
  readonly matcher: ReturnType<typeof createQueryMatcher>;
  private subscription: Subscription | undefined;
  private source: AliasQueryAccess | undefined;
  private sourceAbort: (() => void) | undefined;
  private index: Index;
  private view: QueryView | undefined;
  private releaseView: (() => void) | undefined;
  private running = false;
  private dirty = true;
  private dead = false;
  private released = false;
  private retryTimer: ReturnType<typeof setTimeout> | undefined;
  private retryDelay = 25;
  private viewHint = false;
  private episode: Omit<ReplicaQueryActivity, 'phase'> | undefined;
  private notified = new Set<Client>();
  constructor(readonly manager: Manager, readonly namespace: string, readonly identity: string,
    readonly query: NormalizedReplicaQuery, private readonly releaseConfig: () => void) {
    this.matcher = createQueryMatcher(query.filters); this.index = this.empty();
  }
  private empty(): Index { return { tree: new OrderedQueryTree((a, b) => compareOrderKeys(a, b, this.query.orderBy)), byKey: new Map() }; }
  private assert() { if (this.dead) throw closedError(); }
  private bind() {
    const access = this.manager.access(this.namespace);
    if (this.source === access) return access;
    this.subscription?.unsubscribe();
    if (this.sourceAbort) this.source?.signal.removeEventListener('abort', this.sourceAbort);
    this.source = access; this.dirty = true;
    this.sourceAbort = () => this.schedule(true);
    access.signal.addEventListener('abort', this.sourceAbort, { once: true });
    this.subscription = access.changes.subscribe({ next: event => {
      if (this.dead) return;
      try {
        if (event.type === 'view') this.viewHint = true;
        else for (const key of event.keys) {
          if (!key.startsWith('d:') && !key.startsWith('m:')) continue;
          const physicalKey = `d:${key.slice(2)}`;
          if (this.pending.has(physicalKey)) continue;
          const releaseCount = this.manager.resources.reserve('queuedKeys', 1);
          let releaseBytes: (() => void) | undefined;
          try { releaseBytes = this.manager.resources.reserve('queuedBytes', physicalKey.length * 2); }
          catch (error) { releaseCount(); throw error; }
          this.pending.set(physicalKey, () => { releaseCount(); releaseBytes!(); });
        }
        this.schedule();
      } catch (error) { this.fail(error); }
    }, error: error => this.fail(error), complete: () => this.schedule() });
    return access;
  }
  schedule(wake = false) {
    if (wake && this.retryTimer !== undefined) { clearTimeout(this.retryTimer); this.retryTimer = undefined; }
    if (this.dead || this.running || this.retryTimer !== undefined) return;
    this.running = true;
    void this.manager.enqueue(async () => {
      try { if (!this.dead) await this.run(); }
      catch (error) {
        if (!this.dead) {
          if (error instanceof QueryContention) this.contended(error);
          else this.fail(error);
        }
      }
      finally {
        this.running = false;
        if (this.dead) this.cleanup();
        else if (this.pending.size || this.dirty || [...this.observers].some(observer => !observer.last)) this.schedule();
        else if (this.viewHint) this.defer(0);
      }
    });
  }
  rebindFrom(access: AliasQueryAccess) {
    if (this.source !== access || this.dead) return;
    try { if (this.manager.access(this.namespace) === access) return; } catch { /* The next run reports the unavailable source. */ }
    this.schedule(true);
  }
  private defer(delay: number) {
    if (this.dead || this.retryTimer !== undefined) return;
    this.retryTimer = setTimeout(() => { this.retryTimer = undefined; this.schedule(); }, delay);
  }
  private reportContention(client: Client) {
    if (!this.episode || this.dead || client.closed || client.access.signal.aborted || this.notified.has(client) ||
        ![...this.observers].some(observer => observer.client === client && !observer.once && !observer.closed)) return;
    this.notified.add(client);
    try { client.onActivity?.(Object.freeze({ ...this.episode, phase: 'contended' })); } catch { /* Diagnostics do not own query execution. */ }
  }
  private contended(contention: QueryContention) {
    for (const observer of [...this.observers]) {
      if (!observer.once || observer.closed) continue;
      detach(observer);
      try { observer.error(new QueryBudgetExceeded(contention.kind)); } catch { /* Consumers own their callbacks. */ }
    }
    if (this.dead) return;
    if (!this.episode) this.episode = {
      operationId: crypto.randomUUID(), startedAt: Date.now(),
      code: contention.kind === 'rebuildAttempts' ? 'ReplicaQueryViewContention' : 'ReplicaQueryWorkContention',
    };
    for (const observer of [...this.observers]) { this.reportContention(observer.client); if (this.dead) return; }
    this.defer(this.retryDelay);
    this.retryDelay = Math.min(this.retryDelay * 2, 200);
  }
  private recovered() {
    const episode = this.episode;
    this.episode = undefined; this.retryDelay = 25;
    const clients = [...this.notified]; this.notified.clear();
    if (!episode) return;
    for (const client of clients) {
      if (this.dead || client.closed || client.access.signal.aborted ||
          ![...this.observers].some(observer => observer.client === client && !observer.once && !observer.closed)) continue;
      try { client.onActivity?.(Object.freeze({ ...episode, phase: 'recovered' })); } catch { /* Diagnostics do not own query execution. */ }
    }
  }
  private remove(index: Index, key: string) {
    const old = index.byKey.get(key); if (!old) return;
    index.tree.remove(old.key); index.byKey.delete(key); old.row.release(); old.releaseKey(); old.releaseNode();
  }
  private async update(index: Index, physicalKey: string, view: QueryView, access: AliasQueryAccess) {
    return access.withProjection({ key: physicalKey }, view, async projection => {
      this.assert();
      let replacement: Candidate | undefined;
      if (projection?.visible) {
        const row = this.manager.row(this.namespace, view, projection);
        if (row) {
          let releaseKey: (() => void) | undefined; let releaseNode: (() => void) | undefined;
          try {
            if ((!row.document.deleted || this.query.showDeleted) && this.matcher(row.document)) {
              releaseKey = this.manager.resources.reserve('keyBytes', estimateOrderKeyBytes(row.document, this.query.orderBy) + physicalKey.length * 2 + 64);
              const key = makeOrderKey(row.document, this.query.orderBy);
              releaseNode = this.manager.resources.reserve('nodes', 1);
              replacement = { row, key, physicalKey, releaseKey, releaseNode };
            } else row.release();
          } catch (error) { releaseKey?.(); releaseNode?.(); row.release(); throw error; }
        }
      }
      this.remove(index, physicalKey);
      if (replacement) { index.tree.insert(replacement.key, replacement); index.byKey.set(physicalKey, replacement); }
      return projection?.encodedBytes ?? 0;
    });
  }
  private pop(): string | undefined {
    const entry = this.pending.entries().next().value as [string, () => void] | undefined;
    if (!entry) return;
    this.pending.delete(entry[0]); entry[1](); return entry[0];
  }
  private async run() {
    this.viewHint = false;
    let workRows = 0; let workBytes = 0; let lastYield = performance.now();
    const admitWork = () => {
      if (workRows >= this.manager.limits.scanCandidates) throw new QueryContention('scanCandidates');
      if (workBytes >= this.manager.limits.scanBytes) throw new QueryContention('scanBytes');
    };
    const yieldCPU = async () => { if (performance.now() - lastYield >= 8) { await pause(); lastYield = performance.now(); this.assert(); } };
    for (let attempt = 0; attempt < this.manager.limits.rebuildAttempts; attempt++) {
      this.assert();
      let access: AliasQueryAccess | undefined;
      let index: Index | undefined;
      let installed = true;
      let releaseTarget: (() => void) | undefined;
      try {
        access = this.bind();
        const target = await access.view(); this.assert();
        releaseTarget = this.manager.resources.reserve('keyBytes', 64 + 2 * (target.physicalEpoch.length + (target.sourceGeneration?.length ?? 0) + target.visibilityHash.length));
        const rebuild = this.dirty || !sameView(this.view, target);
        index = rebuild ? this.empty() : this.index;
        this.dirty = false; installed = !rebuild;
        if (rebuild) {
          let scanned = 0; let scannedBytes = 0;
          let after: string | undefined;
          while (true) {
            const page = await access.scan(after, target); this.assert();
            for (const row of page.rows) {
              if (++scanned > this.manager.limits.scanCandidates) throw new QueryBudgetExceeded('scanCandidates');
              scannedBytes += row.encodedBytes;
              if (scannedBytes > this.manager.limits.scanBytes) throw new QueryBudgetExceeded('scanBytes');
              admitWork(); workRows++; workBytes += row.encodedBytes;
              await this.update(index, row.key, target, access); await yieldCPU();
            }
            if (page.done) break;
            if (page.lastKey === undefined || page.lastKey === after) throw new Error('Replica query scan made no progress');
            after = page.lastKey;
          }
        }
        // A complete scan is retained while pending rows are reconciled over
        // later work rounds. It is never published before the final view fence.
        if (!installed) { releaseIndex(this.index); this.index = index; installed = true; }
        if (releaseTarget) {
          this.releaseView?.(); this.releaseView = releaseTarget; releaseTarget = undefined;
          this.view = target;
        }
        while (true) {
          while (this.pending.size) {
            admitWork();
            const key = this.pop()!;
            workRows++; workBytes += await this.update(index, key, target, access); await yieldCPU();
          }
          const current = await access.view(); this.assert();
          if (!sameView(target, current) || this.dirty) { this.dirty = true; break; }
          if (this.pending.size) continue;
          if (await this.publish(access, target)) { this.recovered(); return; }
          if (this.dirty) break;
        }
      } catch (error) {
        if (error instanceof QueryViewChangedError || access?.signal.aborted && error === access.signal.reason && this.hasAlternative(access)) { this.dirty = true; continue; }
        throw error;
      } finally { releaseTarget?.(); if (!installed && index) { releaseIndex(index); this.dirty = true; } }
    }
    throw new QueryContention('rebuildAttempts');
  }
  private remember(observer: Observer, selected: readonly Candidate[]) {
    if (observer.last?.length === selected.length && observer.last.every((row, index) => row === selected[index].row)) return;
    const releaseLast = this.manager.resources.reserve('payloadBytes', 64 + selected.length * 8);
    const previous = observer.last;
    const previousRelease = observer.releaseLast;
    observer.last = selected.map(item => { item.row.refs++; return item.row; });
    observer.releaseLast = releaseLast;
    if (previous) for (const row of previous) row.release();
    previousRelease();
  }
  private hasAlternative(access: AliasQueryAccess) {
    try { return this.manager.access(this.namespace) !== access; } catch { return false; }
  }
  private async publish(access: AliasQueryAccess, view: QueryView): Promise<boolean> {
    let yieldedAt = performance.now();
    const yieldCPU = async () => { if (performance.now() - yieldedAt >= 8) { await pause(); yieldedAt = performance.now(); this.assert(); } };
    for (const observer of [...this.observers]) {
      if (observer.closed || observer.client.closed || observer.client.access.signal.aborted) { detach(observer); continue; }
      try {
      const selected: Candidate[] = []; let more = false; let outputBytes = 2;
      for (const [, candidate] of this.index.tree.entries(observer.query.startAfter ?? undefined)) {
        if (observer.query.limit !== undefined && selected.length === observer.query.limit) { more = true; break; }
        outputBytes += candidate.row.bytes + (selected.length ? 1 : 0);
        if (outputBytes > this.manager.limits.outputBytes) throw new QueryBudgetExceeded('outputBytes');
        selected.push(candidate);
        await yieldCPU();
      }
      const unchanged = observer.last !== undefined && observer.last.length === selected.length &&
        observer.last.every((row, i) => row === selected[i].row || equalResult(row.document, selected[i].row.document));
      if (!observer.once && unchanged) {
        this.remember(observer, selected);
        continue;
      }
      const documents: ReplicaDocument[] = [];
      for (const item of selected) { documents.push(structuredClone(item.row.document)); await yieldCPU(); }
      const latest = await access.view(); this.assert();
      if (!sameView(view, latest) || this.dirty) { this.dirty = true; return false; }
      if (this.pending.size) return false;
      if (observer.closed || observer.client.closed || observer.client.access.signal.aborted) { detach(observer); continue; }
      const page: ReplicaQueryPage = { documents,
        nextCursor: observer.wantsCursor && more && selected.length ? encodeQueryCursor(selected[selected.length - 1].key, observer.query) : null,
        effectiveOrder: observer.query.orderBy.map(order => ({ ...order })) };
      // Last-result references are owned independently from index candidates.
      this.remember(observer, selected);
      try { observer.result(page); } catch (error) { try { observer.error(error); } catch { /* Observers own their callback failures. */ } detach(observer); }
      if (observer.once) detach(observer);
      } catch (error) {
        if (!observer.once || error instanceof QueryViewChangedError || access.signal.aborted) throw error;
        try { observer.error(error); } finally { detach(observer); }
      }
    }
    return true;
  }
  add(observer: Observer) {
    this.observers.add(observer); observer.client.observers.add(observer);
    this.reportContention(observer.client);
    if (!this.dead) this.schedule();
  }
  delete(observer: Observer) {
    this.observers.delete(observer);
    if (![...this.observers].some(current => current.client === observer.client && !current.once)) this.notified.delete(observer.client);
    if (!this.observers.size) this.dispose();
  }
  fail(error: unknown) {
    if (this.dead) return;
    const observers = [...this.observers];
    this.dispose();
    for (const observer of observers) { detach(observer); try { observer.error(error); } catch { /* Observer errors cannot affect another query. */ } }
  }
  private dispose() {
    if (this.dead) return; this.dead = true;
    if (this.retryTimer !== undefined) { clearTimeout(this.retryTimer); this.retryTimer = undefined; }
    this.episode = undefined; this.notified.clear();
    this.subscription?.unsubscribe(); if (this.sourceAbort) this.source?.signal.removeEventListener('abort', this.sourceAbort);
    for (const release of this.pending.values()) release(); this.pending.clear();
    if (!this.running) this.cleanup();
    this.manager.states.delete(this.identity); this.manager.activity();
  }
  private cleanup() {
    if (this.released) return; this.released = true;
    releaseIndex(this.index); this.releaseView?.(); this.releaseView = undefined; this.releaseConfig();
  }
}

const detach = (observer: Observer) => {
  if (observer.closed) return; observer.closed = true;
  if (observer.last) { for (const row of observer.last) row.release(); observer.last = undefined; }
  observer.releaseLast();
  observer.releaseCursor();
  observer.client.observers.delete(observer);
  observer.client.manager.states.get(`${observer.client.access.namespace}:${observer.query.canonicalKey}`)?.delete(observer);
};

export const createReplicaQueryClient = (storage: AliasStorage, options: { limits?: Partial<QueryLimits>; onActivity?: (event: ReplicaQueryActivity) => void } = {}): ReplicaQueryClient => {
  const limits = validateQueryLimits(options.limits);
  let manager = managers.get(storage.databaseNamespace);
  if (manager && Object.keys(limits).some(key => limits[key as keyof QueryLimits] !== manager!.limits[key as keyof QueryLimits])) {
    throw new TypeError('Replica database query handles must use identical resource limits');
  }
  if (!manager) { manager = new Manager(storage.databaseNamespace, limits); managers.set(storage.databaseNamespace, manager); }
  let access: AliasQueryAccess;
  try { access = storage.queryAccess(manager.budget); access.signal.throwIfAborted(); }
  catch (error) { manager.activity(); throw error; }
  const client: Client = { manager, access, observers: new Set(), pending: new Set(), preparations: new Set(), closed: false, abort: () => { void close(); }, onActivity: options.onActivity };
  manager.clients.add(client);
  const close = async () => {
    if (!client.closed) {
      client.closed = true; access.signal.removeEventListener('abort', client.abort);
      for (const cancel of [...client.pending]) cancel();
      for (const observer of [...client.observers]) { if (observer.once) observer.error(access.signal.reason ?? closedError()); detach(observer); }
      for (const state of manager!.states.values()) state.rebindFrom(access);
      manager!.activity();
    }
    await Promise.all(client.preparations);
    await manager!.enqueue(async () => undefined);
    manager!.clients.delete(client); manager!.activity();
  };
  access.signal.addEventListener('abort', client.abort, { once: true });
  const subscribe = (spec: ReplicaQuerySpec, mode: 'get' | 'watch', wantsCursor: boolean, result: (page: ReplicaQueryPage) => void, error: (error: unknown) => void) => {
    let cancelled = false; let observer: Observer | undefined; let release: (() => void) | undefined;
    const cancel = () => { if (cancelled) return; cancelled = true; if (observer) detach(observer); else if (mode === 'get') error(access.signal.reason ?? closedError()); client.pending.delete(cancel); };
    client.pending.add(cancel);
    const preparation = (async () => {
      try {
        if (client.closed) throw closedError(); access.signal.throwIfAborted();
        release = manager!.resources.reserve('keyBytes', estimateQuerySpecBytes(spec));
        const query = await normalizeReplicaQuery(spec, access.namespace, mode);
        if (cancelled || client.closed) return;
        const identity = `${access.namespace}:${query.canonicalKey}`;
        const releaseCursor = manager!.resources.reserve('keyBytes', query.startAfter?.bytes ?? 0);
        let state = manager!.states.get(identity);
        if (!state) { state = new QueryState(manager!, access.namespace, identity, query, release); release = undefined; manager!.states.set(identity, state); manager!.activity(); }
        observer = { client, releaseCursor, releaseLast: () => undefined, wantsCursor, query: Object.freeze({ ...state.query, limit: query.limit, startAfter: query.startAfter }), result, error, once: mode === 'get', last: undefined, closed: false };
        state.add(observer);
      } catch (cause) { if (!cancelled) { cancelled = true; try { error(cause); } catch { /* Consumer error callbacks are isolated. */ } } }
      finally { release?.(); client.pending.delete(cancel); }
    })();
    client.preparations.add(preparation);
    void preparation.finally(() => client.preparations.delete(preparation));
    return cancel;
  };
  return {
    get: (spec = {}) => new Promise<ReplicaDocument[]>((resolve, reject) => { subscribe(spec, 'get', false, page => resolve(page.documents), reject); }),
    getPage: (spec = {}) => new Promise<ReplicaQueryPage>((resolve, reject) => { subscribe(spec, 'get', true, resolve, reject); }),
    watch: (spec, onResult, onError = () => undefined) => subscribe(spec, 'watch', false, page => onResult(page.documents), onError),
    close, debugStats: () => manager!.stats()
  };
};
