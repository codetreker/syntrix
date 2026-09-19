import { expect, test, spyOn } from 'bun:test';
import { Subject } from 'rxjs';
import { createReplicaQueryClient } from './query.js';
import { QueryViewChangedError, type AliasQueryAccess, type QueryView } from './query-source.js';
import type { AliasInvalidation, AliasStorage } from './storage.js';
import type { ReplicaDocument } from './storage-types.js';
const wait = async (condition: () => boolean) => {
  const deadline = Date.now() + 2000;
  while (!condition()) { if (Date.now() > deadline) throw new Error('Timed out waiting for query'); await Bun.sleep(2); }
};
const fixture = () => {
  const namespace = crypto.randomUUID(); const events = new Subject<AliasInvalidation>(); const controller = new AbortController();
  let view: QueryView = { physicalEpoch: 'p1', sourceGeneration: 'g1', manifestRevision: 'r1' };
  const rows = new Map<string, { document: ReplicaDocument; revision: number; encodedBytes: number }>();
  const counts = { scans: 0, reads: 0, decodes: 0, views: 0 };
  let beforeView: (() => void) | undefined; let afterScan: (() => void) | undefined; let beforeDecode: (() => void) | undefined;
  const access: AliasQueryAccess = {
    databaseNamespace: namespace, namespace, signal: controller.signal, changes: events,
    view: async () => { counts.views++; beforeView?.(); return { ...view }; },
    scan: async (after, expected) => {
      counts.scans++; if (expected.manifestRevision !== view.manifestRevision) throw new QueryViewChangedError();
      const keys = [...rows.keys()].sort().filter(key => !after || key > after); const chosen = keys.slice(0, 4);
      const page = { view, rows: chosen.map(key => ({ key, encodedBytes: rows.get(key)!.encodedBytes })),
        lastKey: chosen[chosen.length - 1], done: keys.length <= 4 }; afterScan?.(); return page;
    },
    withProjection: async (key, expected, callback) => {
      counts.reads++; if (expected.manifestRevision !== view.manifestRevision) throw new QueryViewChangedError();
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
    changeView: (notify = true) => { view = { ...view, manifestRevision: crypto.randomUUID() }; if (notify) events.next({ type: 'view', physicalEpoch: view.physicalEpoch, activeSourceGeneration: view.sourceGeneration }); }
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
