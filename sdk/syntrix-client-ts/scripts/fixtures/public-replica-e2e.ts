import { SyntrixClient, type ReplicaDatabase, type ReplicaSyncStatus } from '../../src/index.js';

export type PublicReplicaE2EConfig = {
  endpoint: string;
  database: string;
  collection: string;
  largeCollection: string;
  owner: { access_token: string; refresh_token?: string };
};
type Data = { active: boolean; score: number; count?: bigint; note?: string; payload?: string };
const ids = ['a', 'b', 'c', 'd', 'fixture-typed', 'fixture-offline', 'fixture-pin'];
const check: (condition: unknown, message: string) => asserts condition = (condition, message) => {
  if (!condition) throw new Error(message);
};
const wait = async (predicate: () => boolean | Promise<boolean>, label: string) => {
  const deadline = performance.now() + 30_000;
  while (!await predicate()) {
    if (performance.now() > deadline) throw new Error(`Public replica deadline: ${label}`);
    await new Promise(resolve => setTimeout(resolve, 20));
  }
};

/** All data and synchronization calls use the exported SDK surface. The driver owns browser connectivity. */
export const createPublicReplicaE2E = (config: PublicReplicaE2EConfig) => {
  const client = new SyntrixClient(config.endpoint, { database: config.database, auth: {
    token: config.owner.access_token, refreshToken: config.owner.refresh_token,
  } });
  const name = `public-e2e-${crypto.randomUUID()}`;
  let database: ReplicaDatabase;
  let status: ReplicaSyncStatus = { aliases: {} };
  const errors: unknown[] = [];
  const windows: string[][] = [];
  const receipts: Record<string, unknown> = {};
  let unsubscribers: (() => void)[] = [];
  const options = () => ({ name, collections: {
    matching: client.replicate<Data>(config.collection).where('id', 'in', ids).where('active', '==', true),
    window: client.replicate<Data>(config.collection).where('id', 'in', ids).where('active', '==', true).orderBy('score').limit(2),
    large: client.replicate<Data>(config.largeCollection).orderBy('score').limit(6),
  }, sync: { pollIntervalMs: 100, hintDelayMs: 20, retryBaseMs: 100, retryMaxMs: 1000 } });
  const attach = () => {
    unsubscribers.push(database.sync.subscribe(next => { status = next; }, error => errors.push(error)));
    unsubscribers.push(database.collection<Data>('window').orderBy('score').watch(rows => { windows.push(rows.map(row => row.id)); }, error => errors.push(error)));
  };
  const allReady = () => ['matching', 'window', 'large'].every(alias => status.aliases[alias]?.ready);
  const settled = async (alias: string) => wait(() => status.aliases[alias]?.pending === 0 &&
    status.aliases[alias]?.state === 'idle', `${alias} settlement`);
  const expectWindow = (expected: string[]) => wait(() => windows[windows.length - 1]?.join() === expected.join(), `window ${expected.join()}`);
  const remoteById = (id: string) => client.collection<Data & { id: string }>(config.collection).where('id', '==', id).get();
  const close = async () => {
    unsubscribers.forEach(stop => stop()); unsubscribers = [];
    await database?.close();
  };
  return {
    get receipts() { return { ...receipts }; },
    async inspectOfflineState() {
      return { status, errors: errors.map(error => error instanceof Error ? { name: error.name, message: error.message } : String(error)),
        inspection: await database.sync.inspect('matching', { id: 'fixture-offline' }),
        local: await database.collection<Data>('matching').doc('fixture-offline').get() };
    },
    async connected() {
      database = await client.openReplica(options()); attach();
      await wait(allReady, 'three aliases ready'); await expectWindow(['a', 'b']);
      const page = await database.collection<Data>('matching').orderBy('score').limit(2).getPage();
      check(page.documents.map(row => row.id).join() === 'a,b', 'Local cursor first page differs');
      check(page.nextCursor !== null, 'Local page must expose continuation');
      const next = await database.collection<Data>('matching').orderBy('score').limit(2).startAfter(page.nextCursor).getPage();
      check(next.documents.map(row => row.id).join() === 'c', 'Local continuation differs');
      const large = await database.collection<Data>('large').orderBy('score').get();
      check(large.length === 6 && large.every(row => 'payload' in row && row.payload?.length === 900 * 1024), 'Large gRPC window differs');
      receipts.initial = { cursorPages: [page.documents.length, next.documents.length], largeDocuments: large.length,
        largePayloadBytes: large.reduce((total, row) => total + ('payload' in row ? row.payload?.length ?? 0 : 0), 0) };

      await client.doc<Data>(`${config.collection}/c`).update({ score: 5 }); await expectWindow(['c', 'a']);
      await client.doc<Data>(`${config.collection}/a`).update({ active: false }); await expectWindow(['c', 'b']);
      await wait(async () => await database.collection('matching').doc('a').get() === null, 'filtered leave');
      receipts.windowReplacement = windows.map(value => [...value]);

      const typed = database.collection<Data>('matching').doc('fixture-typed');
      await typed.set({ active: true, score: 90, count: 9007199254740993n });
      await settled('matching');
      await wait(async () => (await remoteById('fixture-typed'))[0]?.count === 9007199254740993n, 'typed upstream round trip');
      await typed.delete(); await settled('matching');
      await wait(async () => (await remoteById('fixture-typed')).length === 0, 'remote deletion');
      await typed.set({ active: true, score: 90, count: 9007199254740994n }); await settled('matching');
      await wait(async () => (await remoteById('fixture-typed'))[0]?.count === 9007199254740994n, 'same ID recreation');
      receipts.typedRecreate = { id: typed.id, exactInt64: true };

      await database.sync.pause('matching');
      await database.collection<Data>('matching').doc('fixture-pin').set({ active: true, score: 95, note: 'pending' });
      check(await database.collection('window').doc('fixture-pin').get() === null, 'Aliases shared unsent desired data');
      check((await remoteById('fixture-pin')).length === 0, 'Paused alias unexpectedly dispatched');
      await database.sync.resume('matching'); await settled('matching');
      receipts.aliasIsolation = true;

      await database.sync.pause('matching');
      const leaving = database.collection<Data>('matching').doc('b');
      await leaving.update({ active: false, note: 'pending source exit' });
      const pending = await leaving.get();
      check(pending && 'active' in pending && pending.active === false, 'Pending edit was removed by its local source predicate');
      check((await remoteById('b'))[0]?.active === true, 'Paused source-exit edit dispatched');
      await database.sync.resume('matching');
      await wait(async () => (await remoteById('b'))[0]?.active === false && await leaving.get() === null, 'source confirms pending departure');
      receipts.filteredPendingLeave = true;
      check(errors.length === 0, 'Unexpected watch error');
      return { ...receipts };
    },
    async offlineEditAndReopen() {
      await database.collection<Data>('matching').doc('fixture-offline').set({ active: true, score: 80, count: 9007199254740995n });
      await close();
      database = await client.openReplica(options()); attach();
      const document = await database.collection<Data>('matching').doc('fixture-offline').get();
      check(document && 'count' in document && document.count === 9007199254740995n, 'Offline reopen lost desired document');
      receipts.offlineReopen = true;
      return { ...receipts };
    },
    async reconnected() {
      await wait(allReady, 'reconnected readiness'); await settled('matching');
      await wait(async () => (await remoteById('fixture-offline'))[0]?.count === 9007199254740995n, 'offline edit uploaded');
      receipts.offlineUpload = true;
      check(errors.length === 0, 'Unexpected watch error');
      return { ...receipts };
    },
    close,
  };
};
