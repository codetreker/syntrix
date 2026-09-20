import { afterEach, describe, expect, it, spyOn } from 'bun:test';
import axios from 'axios';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { SyntrixClient } from './syntrix-client.js';
import { AuthSessionChangedError } from '../api/errors.js';
import type { ReplicaDatabase, OpenReplicaOptions } from '../api/replica-types.js';
import type { ReplicaOptionsSnapshot } from '../api/replica-reference.js';
import type { ReplicaSession } from '../internal/replica/session.js';
import type { DefaultTokenProvider } from '../internal/auth/provider.js';
import * as loader from '../internal/replica/loader.js';
import { createTestLockManager } from '../internal/replica/lock-manager.test-fixture.js';

const jwt = (subject: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub: subject, exp: 0 }))}.sig`.replace(/=/g, '');
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};
type FactoryInput = { session: ReplicaSession; options: ReplicaOptionsSnapshot; endpoint: string; database: string };
const runtime = (open: (input: FactoryInput) => Promise<ReplicaDatabase>) => ({ openReplicaDatabase: open }) as unknown as Awaited<ReturnType<typeof loader.loadReplicaRuntime>>;
const providerOf = (client: SyntrixClient) => Reflect.get(client, 'tokenProvider') as DefaultTokenProvider;
const fakeDatabase = (session: ReplicaSession): ReplicaDatabase => ({
  name: 'cache', sync: {
    pause: async () => {}, resume: async () => {}, resolve: async () => {},
    inspect: async () => ({ issueId: null, stateToken: 'state', physicalEpoch: 'epoch', phase: null, recovering: false, availableActions: [], issues: [], targets: [] }),
    subscribe: () => () => {},
  },
  collection: () => { throw new Error('No storage opened in loader lifecycle tests'); },
  removeCollection: async () => {}, close: () => session.close(),
});

describe('public replica bootstrap', () => {
  const descriptors = new Map<string, PropertyDescriptor | undefined>();
  let loaderSpy: ReturnType<typeof spyOn<typeof loader, 'loadReplicaRuntime'>> | undefined;
  const originalPost = axios.post;
  const setGlobal = (key: string, value: unknown) => {
    if (!descriptors.has(key)) descriptors.set(key, Object.getOwnPropertyDescriptor(globalThis, key));
    Object.defineProperty(globalThis, key, { configurable: true, writable: true, value });
  };
  const browser = () => {
    setGlobal('window', {}); setGlobal('document', {}); setGlobal('indexedDB', {});
    setGlobal('navigator', { locks: { request: () => { throw new Error('Storage must not open in bootstrap tests'); } } });
  };
  const client = () => new SyntrixClient('https://example.test/prefix', { database: 'app', auth: { token: jwt('alice'), refreshToken: 'refresh' } });
  afterEach(() => {
    loaderSpy?.mockRestore(); loaderSpy = undefined;
    axios.post = originalPost;
    for (const [key, descriptor] of descriptors) {
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
      else Reflect.deleteProperty(globalThis, key);
    }
    descriptors.clear();
  });

  it('keeps source construction and remote references available outside browsers and rejects only open', async () => {
    setGlobal('window', undefined);
    const value = client();
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockImplementation(async () => { throw new Error('Must not import'); });
    expect(value.collection('users').path).toBe('users');
    const source = value.replicate('users').where('active', '==', true);
    const error = await value.openReplica({ name: 'cache', collections: { users: source } }).catch(error => error);
    expect(error.code).toBe('ReplicaUnsupportedEnvironment'); expect(loaderSpy).not.toHaveBeenCalled();
  });

  it('checks all browser capabilities before loading storage', async () => {
    browser();
    const value = client(), options = { name: 'cache', collections: { users: value.replicate('users') } };
    for (const [key, missing] of [['document', undefined], ['indexedDB', undefined], ['navigator', {}], ['crypto', {}]] as const) {
      const descriptor = Object.getOwnPropertyDescriptor(globalThis, key);
      setGlobal(key, missing);
      await expect(value.openReplica(options)).rejects.toThrow('requires a browser');
      if (descriptor) Object.defineProperty(globalThis, key, descriptor);
    }
  });

  it('freezes options before lazy import and composes the captured account and configured namespace', async () => {
    browser();
    const value = client(), entered = deferred(), release = deferred();
    const operands = [1n, 2];
    const options: OpenReplicaOptions = { name: 'cache', collections: { users: value.replicate('users').where('score', 'in', operands) }, sync: { pollIntervalMs: 20 } };
    let captured: FactoryInput | undefined;
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockImplementation(async () => {
      entered.resolve(); await release.promise;
      return runtime(async input => { captured = input; return fakeDatabase(input.session); });
    });
    const opening = value.openReplica(options);
    options.name = 'other'; options.sync!.pollIntervalMs = 999; operands[0] = 4n;
    options.collections.users = value.replicate('other');
    await entered.promise; release.resolve();
    const replica = await opening;
    expect(captured!.session.subject).toBe('alice'); expect(captured!.options.name).toBe('cache');
    expect(captured!.options.sync!.pollIntervalMs).toBe(20);
    expect(captured!.options.collections.users.filters[0].value).toEqual([1n, 2]);
    expect(captured!.options.collections.users.collection).toBe('users');
    expect(captured!.endpoint).toBe('https://example.test/prefix'); expect(captured!.database).toBe('app');
    await replica.close();
  });

  it('rejects immediate account replacement before importing the replica runtime', async () => {
    browser();
    const value = client();
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockImplementation(async () => { throw new Error('Must not import'); });
    const pending = value.openReplica({ name: 'cache', collections: { users: value.replicate('users') } }).catch(error => error);
    providerOf(value).setToken(jwt('bob'));
    expect(await pending).toBeInstanceOf(AuthSessionChangedError); expect(loaderSpy).not.toHaveBeenCalled();
  });

  it('owns changed-subject refresh while the lazy chunk is still loading', async () => {
    browser();
    const value = client(), entered = deferred(), release = deferred();
    let opened = false;
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockImplementation(async () => {
      entered.resolve(); await release.promise;
      return runtime(async input => { opened = true; return fakeDatabase(input.session); });
    });
    const pending = value.openReplica({ name: 'cache', collections: { users: value.replicate('users') } }).catch(error => error);
    await entered.promise;
    axios.post = (async () => ({ data: { access_token: jwt('bob'), refresh_token: 'next' } })) as typeof axios.post;
    await expect(providerOf(value).refreshToken()).rejects.toBeInstanceOf(AuthSessionChangedError);
    release.resolve();
    expect(await pending).toBeInstanceOf(AuthSessionChangedError); expect(opened).toBe(false);
    expect(await providerOf(value).getToken()).toBe(jwt('bob'));
  });

  it('permits same-subject refresh during import and closes after failed initialization', async () => {
    browser();
    const value = client(), entered = deferred(), release = deferred(), failure = new Error('opening failed');
    let closed = 0;
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockImplementation(async () => {
      entered.resolve(); await release.promise;
      return runtime(async input => {
        input.session.register({ invalidate: () => {}, close: async () => { closed++; } });
        throw failure;
      });
    });
    const pending = value.openReplica({ name: 'cache', collections: { users: value.replicate('users') } }).catch(error => error);
    await entered.promise;
    axios.post = (async () => ({ data: { access_token: jwt('alice') } })) as typeof axios.post;
    await expect(providerOf(value).refreshToken()).resolves.toBe(jwt('alice'));
    release.resolve();
    expect(await pending).toBe(failure); expect(closed).toBe(1);
    providerOf(value).setToken(jwt('bob'));
    expect(await providerOf(value).getToken()).toBe(jwt('bob'));
  });

  it('preserves both initialization and cleanup errors without hiding the failed owner', async () => {
    browser();
    const value = client(), failure = new Error('open failed'), cleanup = new Error('close failed');
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockResolvedValue(runtime(async input => {
      input.session.register({ invalidate: () => {}, close: async () => { throw cleanup; } });
      throw failure;
    }));
    const error = await value.openReplica({ name: 'cache', collections: { users: value.replicate('users') } }).catch(error => error);
    expect(error.cause).toBe(failure); expect(error.cleanupErrors).toEqual([cleanup]);
    providerOf(value).setToken(jwt('bob'));
    await expect(providerOf(value).getToken()).rejects.toBe(cleanup);
  });

  it('drains the session only after the failed factory has released its own resources', async () => {
    browser();
    const value = client(), failure = new Error('runtime initialization failed');
    let cleaned = false;
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockResolvedValue(runtime(async input => {
      const closeOwned = async () => { cleaned = true; };
      const unregister = input.session.register({ invalidate: () => {}, close: closeOwned });
      await closeOwned(); unregister();
      throw failure;
    }));
    expect(await value.openReplica({ name: 'cache', collections: { users: value.replicate('users') } }).catch(error => error)).toBe(failure);
    expect(cleaned).toBe(true);
  }, 1000);

  it('opens an empty configured replica to remove an omitted historical alias', async () => {
    browser();
    const value = client();
    const removed: string[] = [];
    loaderSpy = spyOn(loader, 'loadReplicaRuntime').mockResolvedValue(runtime(async input => {
      expect(input.options.collections).toEqual({});
      return { ...fakeDatabase(input.session), removeCollection: async alias => { removed.push(alias); } };
    }));
    const replica = await value.openReplica({ name: 'cache', collections: {} });
    await replica.removeCollection('historical');
    expect(removed).toEqual(['historical']);
    await replica.close();
  });

  it('loads the real replica runtime and reopens durable public records without vendor injection', async () => {
    setGlobal('window', {});
    setGlobal('document', { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} });
    setGlobal('indexedDB', indexedDB); setGlobal('IDBKeyRange', IDBKeyRange);
    setGlobal('navigator', { locks: createTestLockManager(), onLine: false });
    const server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: () =>
      Response.json({ code: 'UNAVAILABLE', message: 'Offline bootstrap fixture' }, { status: 503 }) });
    const value = new SyntrixClient(server.url.href, { database: 'app', auth: { token: jwt('alice') } });
    const options = { name: crypto.randomUUID(), collections: { users: value.replicate('users') } };
    let first: ReplicaDatabase | undefined, reopened: ReplicaDatabase | undefined;
    try {
      first = await value.openReplica(options);
      await first.sync.pause();
      const users = first.collection('users');
      await users.doc('alice').set({ exact: 9007199254740993n });
      expect(await users.get()).toEqual([expect.objectContaining({ id: 'alice', exact: 9007199254740993n })]);
      await first.close();
      expect(() => users.doc('alice')).toThrow('closed');
      reopened = await value.openReplica(options);
      await reopened.sync.pause();
      expect(await reopened.collection('users').doc('alice').get()).toMatchObject({ exact: 9007199254740993n });
      expect((await reopened.sync.inspect('users', { id: 'alice' })).document?.desired?.existence).toBe('live');
    } finally {
      const results = await Promise.allSettled([first?.close(), reopened?.close()]);
      server.stop(true);
      for (const result of results) if (result.status === 'rejected') throw result.reason;
    }
  }, 10_000);
});
