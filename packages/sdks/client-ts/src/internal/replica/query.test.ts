import { expect, test, spyOn } from 'bun:test';
import { Subject } from 'rxjs';
import { createReplicaQueryClient, type ReplicaQueryActivity } from './query.js';
import { QueryViewChangedError, type AliasQueryAccess, type QueryView } from './query-source.js';
import type { AliasInvalidation, AliasStorage } from './storage.js';
import type { ReplicaDocument } from './storage-types.js';
const wait = async (condition: () => boolean) => {
  const deadline = Date.now() + 2000;
  while (!condition()) { if (Date.now() > deadline) throw new Error('Timed out waiting for query'); await Bun.sleep(2); }
};
const fixture = () => {
  const namespace = crypto.randomUUID(); const events = new Subject<AliasInvalidation>(); const controller = new AbortController();
  let view: QueryView = { physicalEpoch: 'p1', sourceGeneration: 'g1', visibilityHash: 'r1' };
  const rows = new Map<string, { document: ReplicaDocument; revision: number; encodedBytes: number }>();
  const counts = { scans: 0, reads: 0, decodes: 0, views: 0 };
  let beforeView: (() => void) | undefined; let afterScan: (() => void) | undefined; let beforeDecode: (() => void) | undefined;
  const access: AliasQueryAccess = {
    databaseNamespace: namespace, namespace, signal: controller.signal, changes: events,
    view: async () => { counts.views++; beforeView?.(); return { ...view }; },
    scan: async (after, expected) => {
      counts.scans++; if (expected.visibilityHash !== view.visibilityHash) throw new QueryViewChangedError();
      const keys = [...rows.keys()].sort().filter(key => !after || key > after); const chosen = keys.slice(0, 4);
      const page = { view, rows: chosen.map(key => ({ key, encodedBytes: rows.get(key)!.encodedBytes })),
        lastKey: chosen[chosen.length - 1], done: keys.length <= 4 }; afterScan?.(); return page;
    },
    withProjection: async (key, expected, callback) => {
      counts.reads++; if (expected.visibilityHash !== view.visibilityHash) throw new QueryViewChangedError();
      const physicalKey = 'key' in key ? key.key : `d:${key.id}`; const row = rows.get(physicalKey);
      return callback(row ? { id: row.document.id, key: physicalKey, revision: String(row.revision), encodedBytes: row.encodedBytes,
        visible: true, decode: () => { beforeDecode?.(); counts.decodes++; return structuredClone(row.document); } } : null);
    }
  };
  const storage = { namespace, databaseNamespace: namespace, queryAccess: () => access } as unknown as AliasStorage;
  const set = (id: string, data: Record<string, any>, notify = true, encodedBytes = 100) => {
    rows.set(`d:${id}`, { document: { id, collection: 'people', ...data } as ReplicaDocument, revision: (rows.get(`d:${id}`)?.revision ?? 0) + 1, encodedBytes });
    if (notify) events.next({ type: 'row', physicalEpoch: view.physicalEpoch, keys: [`d:${id}`] });
  };
  return { storage, access, rows, counts, controller, events, set,
    setBeforeView: (hook?: () => void) => { beforeView = hook; }, setAfterScan: (hook?: () => void) => { afterScan = hook; },
    setBeforeDecode: (hook?: () => void) => { beforeDecode = hook; },
    changeView: (notify = true) => { view = { ...view, visibilityHash: crypto.randomUUID() }; if (notify) events.next({ type: 'view', physicalEpoch: view.physicalEpoch, activeSourceGeneration: view.sourceGeneration }); }
  };
};

test('window maintenance reads changed IDs only and fills from complete candidates', async () => {
  const env = fixture(); for (let i = 0; i < 12; i++) env.set(String(i).padStart(2, '0'), { score: i }, false);
  const client = createReplicaQueryClient(env.storage); const results: string[][] = []; const errors: unknown[] = [];
  const stop = client.watch({ orderBy: [{ field: 'score', direction: 'asc' }], limit: 2 }, docs => results.push(docs.map(doc => doc.id)), error => errors.push(error));
  try {
    await wait(() => results.length === 1); expect(results[0]).toEqual(['00', '01']); const scans = env.counts.scans;
    env.set('00', { score: 100 }); await wait(() => results.length === 2); expect(results[1]).toEqual(['01', '02']);
    env.set('11', { score: -1 }); await wait(() => results.length === 3); expect(results[2]).toEqual(['11', '01']);
    env.set('10', { score: 10 }); await Bun.sleep(15); expect(results).toHaveLength(3);
    expect(env.counts.scans).toBe(scans); expect(errors).toEqual([]); expect(client.debugStats().nodes).toBe(12);
  } finally { stop(); await client.close(); }
  expect(client.debugStats()).toMatchObject({ nodes: 0, payloadBytes: 0, keyBytes: 0, cacheEntries: 0 });
});

test('canonical observers share tree while distinct orders share decoded rows', async () => {
  const env = fixture(); env.set('a', { score: 1 }, false); env.set('b', { score: 2 }, false);
  const client = createReplicaQueryClient(env.storage); let calls = 0;
  const a = client.watch({ limit: 1 }, () => calls++); const b = client.watch({ limit: 2 }, () => calls++);
  await wait(() => calls === 2); expect(client.debugStats()).toMatchObject({ queries: 1, nodes: 2, cacheEntries: 2 });
  const payload = client.debugStats().payloadBytes; const decodes = env.counts.decodes;
  const c = client.watch({ orderBy: [{ field: 'score', direction: 'desc' }] }, () => calls++);
  await wait(() => calls === 3); expect(client.debugStats()).toMatchObject({ queries: 2, nodes: 4, cacheEntries: 2, payloadBytes: payload + 80 });
  expect(env.counts.decodes).toBe(decodes); a(); b(); expect(client.debugStats().nodes).toBe(2); c(); await client.close();
  expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, payloadBytes: 0, keyBytes: 0 });
});

test('page cursor is keyset and query output cannot mutate cached values', async () => {
  const env = fixture(); for (const id of ['a', 'b', 'c']) env.set(id, { nested: { count: 1n } }, false);
  const client = createReplicaQueryClient(env.storage); const first = await client.getPage({ limit: 2 });
  expect(first.documents.map(doc => doc.id)).toEqual(['a', 'b']); expect(first.nextCursor).not.toBeNull();
  (first.documents[0] as any).nested.count = 9n;
  const second = await client.getPage({ limit: 2, startAfter: first.nextCursor! }); expect(second.documents.map(doc => doc.id)).toEqual(['c']); expect(second.nextCursor).toBeNull();
  expect((await client.get({ limit: 1 }))[0]).toMatchObject({ nested: { count: 1n } });
  await expect(client.get({ filters: [{ field: 'id', op: '==', value: 'b' }], startAfter: first.nextCursor! })).rejects.toThrow('scope');
  await client.close();
});

test('subscribe-before-scan reconciles writes that happened behind the scan cursor', async () => {
  const env = fixture(); env.set('b', { value: 'old' }, false);
  let fired = false; env.setAfterScan(() => { if (!fired) { fired = true; env.set('a', { value: 'new' }); env.set('b', { value: 'new' }); } });
  const client = createReplicaQueryClient(env.storage); const documents = await client.get();
  expect(documents.map(doc => doc.id)).toEqual(['a', 'b']); expect(documents.every(doc => (doc as any).value === 'new')).toBe(true); await client.close();
});

test('repeated authoritative changes terminate within a finite rebuild budget', async () => {
  const env = fixture(); env.set('a', { score: 1 }, false); env.setBeforeView(() => env.changeView(false));
  const client = createReplicaQueryClient(env.storage, { limits: { rebuildAttempts: 3 } });
  await expect(client.get()).rejects.toMatchObject({ code: 'QueryBudgetExceeded', kind: 'rebuildAttempts' });
  expect(env.counts.views).toBe(6); expect(client.debugStats()).toMatchObject({ nodes: 0, payloadBytes: 0, keyBytes: 0 }); await client.close();
});

test('scan and output budgets fail without leaking persistent references', async () => {
  for (const limits of [{ scanCandidates: 1 }, { scanBytes: 150 }, { outputBytes: 10 }]) {
    const env = fixture(); env.set('a', { score: 1 }, false); env.set('b', { score: 2 }, false);
    const client = createReplicaQueryClient(env.storage, { limits }); await expect(client.get()).rejects.toMatchObject({ code: 'QueryBudgetExceeded' });
    expect(client.debugStats()).toMatchObject({ nodes: 0, payloadBytes: 0, keyBytes: 0, cacheEntries: 0 }); await client.close();
  }
});

test('decode reservation is present before decode and growth accounts old and new payload', async () => {
  const env = fixture(); env.set('a', { score: 1 }, false); const client = createReplicaQueryClient(env.storage, { limits: { payloadBytes: 1800 } });
  env.setBeforeDecode(() => expect(client.debugStats().payloadBytes).toBeGreaterThanOrEqual(1056));
  const results: ReplicaDocument[][] = []; const errors: any[] = []; client.watch({}, docs => results.push(docs), error => errors.push(error));
  await wait(() => results.length === 1); env.set('a', { score: 2 }); await wait(() => errors.length === 1);
  expect(errors[0]).toMatchObject({ kind: 'payloadBytes' }); expect(results).toHaveLength(1); expect(client.debugStats().payloadBytes).toBe(0); await client.close();
});

test('callback exceptions isolate an observer from its canonical peers', async () => {
  const env = fixture(); env.set('a', {}, false); const client = createReplicaQueryClient(env.storage); let good = 0; const errors: unknown[] = [];
  const failure = new Error('business callback'); client.watch({}, () => { throw failure; }, error => errors.push(error));
  const stop = client.watch({}, () => good++); await wait(() => good === 1); expect(errors).toEqual([failure]);
  env.set('b', {}); await wait(() => good === 2); stop(); await client.close();
});

test('client close rejects an in-flight get and drains normalization reservations', async () => {
  const env = fixture(); env.set('a', {}, false); const client = createReplicaQueryClient(env.storage);
  const pending = client.get().then(value => ({ value }), error => ({ error })); await client.close(); expect(await pending).toMatchObject({ error: { code: 'ReplicaQueryClosed' } });
  expect(client.debugStats()).toMatchObject({ nodes: 0, payloadBytes: 0, keyBytes: 0 }); await expect(client.get()).rejects.toMatchObject({ code: 'ReplicaQueryClosed' });
});

test('closing manager retains shared budget until in-flight reads drain', async () => {
 const env=fixture(); env.set('a', {}, false); const budgets:any[]=[];
 let release!:()=>void; const gate=new Promise<void>(r=>release=r); let active=0,peak=0,starts=0;
 (env.storage as any).queryAccess=(budget:any)=> { budgets.push(budget); return {...env.access,view:()=>budget.withReservation(64,async()=>{ starts++; active+=64;peak=Math.max(peak,active); await gate; active-=64;return env.access.view();})}; };
 const options={limits:{readBytes:64}};
 const first=createReplicaQueryClient(env.storage,options); first.watch({},()=>{});
 await wait(()=>starts===1); const closed=first.close();
 const second=createReplicaQueryClient(env.storage,options); second.watch({},()=>{});
 await Bun.sleep(10);
 release(); await closed; await second.close(); expect(peak).toBeLessThanOrEqual(64);
});

test('ordinary writes during verification remain incremental', async()=>{
 const env=fixture(); for(let i=0;i<12;i++)env.set(String(i).padStart(2,'0'),{score:i},false);
 const client=createReplicaQueryClient(env.storage);let calls=0;const errors:any[]=[];
 client.watch({orderBy:[{field:'score',direction:'asc'}],limit:1},()=>calls++,e=>errors.push(e));
 await wait(()=>calls===1);const scans=env.counts.scans;let views=0;
 env.setBeforeView(()=>{if(++views===2)env.set('01',{score:-10});});
 env.set('00',{score:100});await wait(()=>calls===2||errors.length>0);

 await client.close();expect(env.counts.scans).toBe(scans);
});

test('watch must not construct inaccessible cursor', async () => {
 const env = fixture(); env.set('a', { sort: 'x'.repeat(20000) }, false); env.set('b', { sort: 'y' }, false);
 const client=createReplicaQueryClient(env.storage); const results:any[]=[]; const errors:any[]=[];
 client.watch({orderBy:[{field:'sort',direction:'asc'}],limit:1}, d=>results.push(d), e=>errors.push(e));
 await wait(()=>results.length>0||errors.length>0);

 expect(results).toHaveLength(1); await client.close();
});


test('oversized page cursor rejects only its request while get and watch remain valid', async () => {
  const env = fixture(); env.set('a', { sort: 'x'.repeat(20000) }, false, 20100); env.set('b', { sort: 'y' }, false);
  const client = createReplicaQueryClient(env.storage); const spec = { orderBy: [{ field: 'sort', direction: 'asc' as const }], limit: 1 };
  let calls = 0; const errors: unknown[] = []; client.watch(spec, () => calls++, error => errors.push(error)); await wait(() => calls === 1);
  expect((await client.get(spec))[0].id).toBe('a'); await expect(client.getPage(spec)).rejects.toThrow('cursor exceeds');
  env.set('a', { sort: 'w'.repeat(20000) }, true, 20100); await wait(() => calls === 2); expect(errors).toEqual([]); await client.close();
});

test('queue admission coalesces keys and releases a failed query without altering stored rows', async () => {
  const env = fixture(); env.set('a', {}, false); const client = createReplicaQueryClient(env.storage, { limits: { queuedKeys: 2 } });
  let calls = 0; const errors: any[] = []; client.watch({}, () => calls++, error => errors.push(error)); await wait(() => calls === 1);
  env.set('b', {}); env.set('b', {}); expect(client.debugStats().queuedKeys).toBe(1); env.set('c', {}); env.set('d', {});
  await wait(() => errors.length === 1); await client.close(); expect(errors[0]).toMatchObject({ kind: 'queuedKeys' });
  expect(client.debugStats()).toMatchObject({ payloadBytes: 0, keyBytes: 0, queuedBytes: 0, queuedKeys: 0, nodes: 0 }); expect(env.rows.size).toBe(4);
});

test('closing the selected source during its initial view read rebinds to the surviving handle', async () => {
  const env = fixture(); env.set('a', {}, false); let release!: () => void; const gate = new Promise<void>(resolve => { release = resolve; });
  let started = false; const originalView = env.access.view;
  env.access.view = async () => { started = true; await gate; env.controller.signal.throwIfAborted(); return originalView(); };
  const first = createReplicaQueryClient(env.storage); const errors: unknown[] = []; first.watch({}, () => undefined, error => errors.push(error));
  const secondController = new AbortController(); const secondAccess = { ...env.access, signal: secondController.signal, view: originalView };
  const second = createReplicaQueryClient({ ...env.storage, queryAccess: () => secondAccess } as unknown as AliasStorage);
  const values: ReplicaDocument[][] = []; second.watch({}, docs => values.push(docs), error => errors.push(error));
  await wait(() => started); env.controller.abort(new Error('old owner closed')); release(); await first.close(); await wait(() => values.length === 1);
  expect(values[0][0].id).toBe('a'); expect(errors).toEqual([]); await second.close();
});

test('retained manifest identity is charged before caching candidate rows', async () => {
  const env = fixture(); const originalView = env.access.view; env.access.view = async () => ({ ...await originalView(), sourceGeneration: 'x'.repeat(5000) });
  env.set('a', {}, false); const client = createReplicaQueryClient(env.storage, { limits: { keyBytes: 10000 } });
  await expect(client.get()).rejects.toMatchObject({ code: 'QueryBudgetExceeded', kind: 'keyBytes' });
  expect(env.counts.decodes).toBe(0); await client.close(); expect(client.debugStats().keyBytes).toBe(0);
});


test('public result equality retains bigint and number distinctions at every nesting level', async () => {
  const env = fixture(); env.set('a', { value: 1n, nested: [1n, { count: 2n }] }, false);
  const client = createReplicaQueryClient(env.storage); const outputs: ReplicaDocument[][] = []; client.watch({}, docs => outputs.push(docs));
  await wait(() => outputs.length === 1); env.set('a', { value: 1, nested: [1n, { count: 2n }] }); await wait(() => outputs.length === 2);
  env.set('a', { value: 1, nested: [1, { count: 2 }] }); await wait(() => outputs.length === 3);
  expect(outputs[0][0]).toMatchObject({ value: 1n }); expect(outputs[2][0]).toMatchObject({ value: 1, nested: [1, { count: 2 }] }); await client.close();
});

test('periodic manifest check recovers a lost generation notification and stops with the last observer', async () => {
  const env = fixture(); env.set('a', {}, false); let tick: (() => void) | undefined;
  const original = globalThis.setInterval;
  const interval = spyOn(globalThis, 'setInterval').mockImplementation(((callback: () => void, delay: number) => {
    expect(delay).toBe(10000); tick = callback; return original(callback, delay);
  }) as typeof setInterval);
  const client = createReplicaQueryClient(env.storage); const outputs: string[][] = [];
  const stop = client.watch({}, docs => outputs.push(docs.map(doc => doc.id)));
  try {
    await wait(() => outputs.length === 1); env.rows.clear(); env.set('b', {}, false); env.changeView(false); tick!();
    await wait(() => outputs.length === 2); expect(outputs[1]).toEqual(['b']);
    stop(); expect(client.debugStats().queries).toBe(0); expect(interval).toHaveBeenCalledTimes(1);
  } finally { interval.mockRestore(); await client.close(); }
});

test('storage failures preserve the original error and release all query charges', async () => {
  const env = fixture(); env.set('a', {}, false); const failure = new Error('disk failed');
  env.access.withProjection = async () => { throw failure; }; const client = createReplicaQueryClient(env.storage);
  await expect(client.get()).rejects.toBe(failure); await client.close(); expect(client.debugStats()).toMatchObject({ payloadBytes: 0, nodes: 0, keyBytes: 0 });
});


test('a watch survives repeated view contention with capped backoff and reports one recovery episode', async () => {
  const env = fixture(); env.set('a', {}, false); let remaining = 80;
  env.setBeforeView(() => { if (remaining-- > 0) env.changeView(false); });
  const delays: number[] = []; const timeout = globalThis.setTimeout;
  const timer = spyOn(globalThis, 'setTimeout').mockImplementation(((callback: (...args: any[]) => void, delay?: number, ...args: any[]) => {
    if (delay !== undefined && delay >= 25 && delay <= 200) delays.push(delay);
    return timeout(callback, delay, ...args);
  }) as typeof setTimeout);
  const activities: ReplicaQueryActivity[] = []; const outputs: ReplicaDocument[][] = []; const errors: unknown[] = [];
  const client = createReplicaQueryClient(env.storage, { onActivity: event => activities.push(event) });
  client.watch({}, docs => outputs.push(docs), error => errors.push(error));
  try {
    await wait(() => outputs.length === 1);
    expect(delays).toEqual([25, 50, 100, 200, 200]); expect(errors).toEqual([]);
    expect(activities.map(event => event.phase)).toEqual(['contended', 'recovered']);
    expect(activities[0].operationId).toBe(activities[1].operationId);
    expect(activities[0].code).toBe('ReplicaQueryViewContention');
    expect(Object.keys(activities[0]).sort()).toEqual(['code', 'operationId', 'phase', 'startedAt']);
    expect(client.debugStats().queries).toBe(1);
  } finally { timer.mockRestore(); await client.close(); }
  expect(client.debugStats()).toMatchObject({ payloadBytes: 0, keyBytes: 0, queuedKeys: 0, nodes: 0 });
});

test('control notifications with unchanged semantic identity do not rebuild or starve row updates', async () => {
  const env = fixture(); env.set('a', { value: 1 }, false);
  env.setBeforeView(() => env.events.next({ type: 'view', physicalEpoch: 'p1', activeSourceGeneration: 'g1' }));
  const activities: ReplicaQueryActivity[] = []; const outputs: ReplicaDocument[][] = [];
  const client = createReplicaQueryClient(env.storage, { onActivity: event => activities.push(event) }); client.watch({}, docs => outputs.push(docs));
  try {
    await wait(() => outputs.length === 1); const scans = env.counts.scans;
    for (let value = 2; value <= 12; value++) { env.set('a', { value }); await wait(() => outputs.length === value); }
    expect(env.counts.scans).toBe(scans); expect(activities).toEqual([]); expect(outputs[11][0]).toMatchObject({ value: 12 });
  } finally { await client.close(); }
});

test('one-off reads fail finitely while the same canonical watch and another alias continue', async () => {
  const env = fixture(); env.set('a', {}, false); env.setBeforeView(() => env.changeView(false));
  const activities: ReplicaQueryActivity[] = []; const outputs: ReplicaDocument[][] = []; const errors: unknown[] = [];
  const client = createReplicaQueryClient(env.storage, { onActivity: event => activities.push(event) });
  client.watch({}, docs => outputs.push(docs), error => errors.push(error));
  const once = client.get().then(value => ({ value }), error => ({ error }));
  try {
    await wait(() => activities.length > 0);
    expect(await once).toMatchObject({ error: { code: 'QueryBudgetExceeded', kind: 'rebuildAttempts' } });
    expect(client.debugStats().queries).toBe(1);
    const other = fixture(); other.set('b', {}, false);
    const independent = createReplicaQueryClient({ ...other.storage, databaseNamespace: env.storage.databaseNamespace } as AliasStorage);
    try { expect((await independent.get())[0].id).toBe('b'); } finally { await independent.close(); }
    env.setBeforeView(undefined); await wait(() => outputs.length === 1);
    expect(errors).toEqual([]); expect(activities.map(event => event.phase)).toEqual(['contended', 'recovered']);
  } finally { await client.close(); }
});

test('discarded scan work yields without converting a legal near-limit view into a terminal budget error', async () => {
  for (const limits of [{ scanCandidates: 8 }, { scanBytes: 800 }]) {
    const env = fixture(); for (let i = 0; i < 6; i++) env.set(String(i), {}, false);
    const original = env.access.withProjection; let changed = false;
    env.access.withProjection = async (key, expected, callback) => {
      const value = await original(key, expected, callback);
      if (!changed && env.counts.reads === 4) { changed = true; env.changeView(false); }
      return value;
    };
    const activities: ReplicaQueryActivity[] = []; const outputs: ReplicaDocument[][] = []; const errors: unknown[] = [];
    const client = createReplicaQueryClient(env.storage, { limits, onActivity: event => activities.push(event) });
    client.watch({}, docs => outputs.push(docs), error => errors.push(error));
    try {
      await wait(() => outputs.length === 1 || errors.length > 0);
      expect(errors).toEqual([]); expect(outputs[0]).toHaveLength(6);
      expect(activities.map(event => event.phase)).toEqual(['contended', 'recovered']);
      expect(activities[0].code).toBe('ReplicaQueryWorkContention');
    } finally { await client.close(); }
    expect(client.debugStats()).toMatchObject({ payloadBytes: 0, keyBytes: 0, nodes: 0 });
  }
});

test('continuous incremental work yields with queued IDs and installed candidates intact', async () => {
  const env = fixture(); env.set('a', { value: 0 }, false); env.set('b', { value: 1 }, false);
  const activities: ReplicaQueryActivity[] = []; const outputs: ReplicaDocument[][] = []; const errors: unknown[] = [];
  const client = createReplicaQueryClient(env.storage, { limits: { scanCandidates: 3 }, onActivity: event => activities.push(event) });
  client.watch({}, docs => outputs.push(docs), error => errors.push(error));
  try {
    await wait(() => outputs.length === 1); const scans = env.counts.scans; let edits = 0;
    env.setBeforeDecode(() => { if (++edits <= 10) env.set('a', { value: edits + 1 }); });
    env.set('a', { value: 1 }); await wait(() => outputs.length === 2 || errors.length > 0);
    expect(errors).toEqual([]); expect(outputs[1][0]).toMatchObject({ value: 11 }); expect(env.counts.scans).toBe(scans);
    expect(activities.map(event => event.phase)).toEqual(['contended', 'recovered']);
    expect(client.debugStats()).toMatchObject({ nodes: 2, queuedKeys: 0 });
  } finally { await client.close(); }
});

test('a full scan at its work limit retains progress before reconciling initialization writes', async () => {
  const env = fixture(); for (let i = 0; i < 4; i++) env.set(String(i), { value: 0 }, false);
  let changed = false; env.setAfterScan(() => { if (!changed) { changed = true; env.set('0', { value: 1 }); } });
  const outputs: ReplicaDocument[][] = []; const errors: unknown[] = []; const activities: ReplicaQueryActivity[] = [];
  const client = createReplicaQueryClient(env.storage, { limits: { scanCandidates: 4 }, onActivity: event => activities.push(event) });
  client.watch({}, docs => outputs.push(docs), error => errors.push(error));
  try {
    await wait(() => outputs.length === 1 || errors.length > 0); expect(errors).toEqual([]); expect(outputs[0][0]).toMatchObject({ value: 1 });
    expect(env.counts.scans).toBe(1); expect(activities.map(event => event.phase)).toEqual(['contended', 'recovered']);
  } finally { await client.close(); }
});

test('stable views exceeding actual scan or output limits still terminate a watch', async () => {
  for (const limits of [{ scanCandidates: 2 }, { scanBytes: 200 }, { outputBytes: 10 }]) {
    const env = fixture(); for (const id of ['a', 'b', 'c']) env.set(id, {}, false);
    const activities: ReplicaQueryActivity[] = []; const errors: any[] = [];
    const client = createReplicaQueryClient(env.storage, { limits, onActivity: event => activities.push(event) });
    client.watch({}, () => { throw new Error('Oversized query must not publish'); }, error => errors.push(error));
    try {
      await wait(() => errors.length > 0); expect(errors[0].code).toBe('QueryBudgetExceeded'); expect(activities).toEqual([]);
      expect(client.debugStats().queries).toBe(0);
    } finally { await client.close(); }
  }
});

test('unsubscribe, close and account cancellation during contention remove delayed work', async () => {
  for (const cancel of ['unsubscribe', 'close', 'account'] as const) {
    const env = fixture(); env.set('a', {}, false); env.setBeforeView(() => env.changeView(false));
    const activities: ReplicaQueryActivity[] = []; const outputs: ReplicaDocument[][] = [];
    const client = createReplicaQueryClient(env.storage, { onActivity: event => activities.push(event) });
    const stop = client.watch({}, docs => outputs.push(docs)); await wait(() => activities.length === 1);
    if (cancel === 'unsubscribe') stop();
    else if (cancel === 'account') env.controller.abort(new Error('Session changed'));
    else await client.close();
    const reads = env.counts.views; env.setBeforeView(undefined); await Bun.sleep(60);
    expect(env.counts.views).toBe(reads); expect(outputs).toEqual([]); expect(activities).toHaveLength(1);
    await client.close(); expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, payloadBytes: 0, keyBytes: 0 });
  }
});

test('activity callbacks can synchronously close their owner without leaving retry work', async () => {
  const env = fixture(); env.set('a', {}, false); env.setBeforeView(() => env.changeView(false));
  let closing: Promise<void> | undefined; let activities = 0;
  const client = createReplicaQueryClient(env.storage, { onActivity: () => { activities++; closing = client.close(); } });
  client.watch({}, () => { throw new Error('Closed observer must not publish'); });
  await wait(() => closing !== undefined); await closing; const reads = env.counts.views; await Bun.sleep(40);
  expect(activities).toBe(1); expect(env.counts.views).toBe(reads); expect(client.debugStats().queries).toBe(0);
});


test('a view notification arriving after the final view snapshot schedules another identity check', async () => {
  const env = fixture(); env.set('a', {}, false); const original = env.access.view; let reads = 0;
  env.access.view = async () => {
    const captured = await original();
    if (++reads === 3) { env.rows.clear(); env.set('b', {}, false); env.changeView(); }
    return captured;
  };
  const outputs: string[][] = []; const client = createReplicaQueryClient(env.storage); client.watch({}, docs => outputs.push(docs.map(doc => doc.id)));
  try { await wait(() => outputs.some(ids => ids[0] === 'b')); expect(outputs[outputs.length - 1]).toEqual(['b']); }
  finally { await client.close(); }
});

test('shared contention diagnostics follow each active client through source handoff', async () => {
  const env = fixture(); env.set('a', {}, false); env.setBeforeView(() => env.changeView(false));
  const firstEvents: ReplicaQueryActivity[] = [], secondEvents: ReplicaQueryActivity[] = []; const errors: unknown[] = [];
  const first = createReplicaQueryClient(env.storage, { onActivity: event => firstEvents.push(event) });
  const otherSignal = new AbortController();
  const secondStorage = { ...env.storage, queryAccess: () => ({ ...env.access, signal: otherSignal.signal }) } as AliasStorage;
  const second = createReplicaQueryClient(secondStorage, { onActivity: event => secondEvents.push(event) });
  first.watch({}, () => undefined, error => errors.push(error)); const outputs: ReplicaDocument[][] = [];
  second.watch({}, docs => outputs.push(docs), error => errors.push(error)); second.watch({ limit: 1 }, () => undefined, error => errors.push(error));
  try {
    await wait(() => firstEvents.length === 1 && secondEvents.length === 1);
    expect(firstEvents[0].operationId).toBe(secondEvents[0].operationId);
    env.setBeforeView(undefined); env.controller.abort(new Error('Retired source')); await first.close(); await wait(() => outputs.length === 1);
    expect(firstEvents.map(event => event.phase)).toEqual(['contended']); expect(secondEvents.map(event => event.phase)).toEqual(['contended', 'recovered']);
    expect(errors).toEqual([]); expect(outputs[0][0].id).toBe('a');
  } finally { await first.close(); await second.close(); }
});

test('storage failure during a retry stays terminal and does not report a false recovery', async () => {
  const env = fixture(); env.set('a', {}, false); env.setBeforeView(() => env.changeView(false)); const failure = new Error('storage failed');
  const activities: ReplicaQueryActivity[] = []; const errors: unknown[] = [];
  const client = createReplicaQueryClient(env.storage, { onActivity: event => {
    activities.push(event); env.setBeforeView(() => { throw failure; }); throw new Error('diagnostic observer failed');
  } });
  client.watch({}, () => undefined, error => errors.push(error));
  try { await wait(() => errors.length === 1); expect(errors).toEqual([failure]); expect(activities.map(event => event.phase)).toEqual(['contended']); }
  finally { await client.close(); }
  expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, payloadBytes: 0, keyBytes: 0 });
});


test('an unrelated storage failure is not retried merely because its source closed concurrently', async () => {
  const env = fixture(); env.set('a', {}, false); let release!: () => void; const gate = new Promise<void>(resolve => { release = resolve; });
  const failure = new Error('storage corruption'); const originalView = env.access.view; let started = false;
  env.access.view = async () => { started = true; await gate; throw failure; };
  const first = createReplicaQueryClient(env.storage); first.watch({}, () => undefined);
  const secondAccess = { ...env.access, signal: new AbortController().signal, view: originalView };
  const second = createReplicaQueryClient({ ...env.storage, queryAccess: () => secondAccess } as AliasStorage);
  const errors: unknown[] = []; const outputs: ReplicaDocument[][] = [];
  second.watch({}, docs => outputs.push(docs), error => errors.push(error));
  try {
    await wait(() => started); env.controller.abort(new Error('Closed source')); release();
    await wait(() => errors.length === 1); expect(errors).toEqual([failure]); expect(outputs).toEqual([]);
  } finally { release(); await first.close(); await second.close(); }
});
