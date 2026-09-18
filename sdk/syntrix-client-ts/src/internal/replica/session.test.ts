import { afterEach, expect, test } from 'bun:test';
import axios from 'axios';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaSession } from './session.js';
import { AuthSessionChangedError } from '../../api/errors.js';
const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub, exp: 0 }))}.sig`.replace(/=/g, '');
const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(r => { resolve = r; }); return { promise, resolve }; };
const originalPost = axios.post;
afterEach(() => { axios.post = originalPost; });

for (const action of ['token', 'refresh-token', 'login', 'logout'] as const) {
  test(`${action} invalidates immediately and drains before admitting credentials`, async () => {
    const provider = new DefaultTokenProvider({ token: jwt('A'), refreshToken: 'refresh' });
    const session = await createReplicaSession(provider);
    const closing = deferred();
    let invalid = false;
    session.register({ invalidate: () => { invalid = true; }, close: () => closing.promise });
    axios.post = (async () => ({ data: { access_token: jwt('B'), refresh_token: 'new' } })) as typeof axios.post;
    let completion: Promise<unknown> = Promise.resolve();
    if (action === 'token') provider.setToken(jwt('B'));
    if (action === 'refresh-token') provider.setRefreshToken('next');
    if (action === 'login') completion = provider.login('B', 'password');
    if (action === 'logout') completion = provider.logout();
    expect(invalid).toBe(true);
    expect(session.signal.aborted).toBe(true);
    expect(() => session.assertCurrent()).toThrow(AuthSessionChangedError);
    let tokenResolved = false;
    const token = provider.getToken().then(value => { tokenResolved = true; return value; });
    await Promise.resolve(); expect(tokenResolved).toBe(false);
    closing.resolve(); await completion; await token;
    expect(await provider.getToken()).toBe(action === 'logout' ? null : action === 'refresh-token' ? jwt('A') : jwt('B'));
  });
}

test('same-subject refresh preserves ownership; different subject drains and fences old requests', async () => {
  const provider = new DefaultTokenProvider({ token: jwt('A'), refreshToken: 'refresh' });
  const session = await createReplicaSession(provider);
  axios.post = (async () => ({ data: { access_token: jwt('A') } })) as typeof axios.post;
  expect(await provider.refreshToken()).toBe(jwt('A'));
  session.assertCurrent();
  const closing = deferred(), invalidated = deferred();
  session.register({ invalidate: invalidated.resolve, close: () => closing.promise });
  axios.post = (async () => ({ data: { access_token: jwt('B') } })) as typeof axios.post;
  const result = provider.refreshToken().catch(error => error);
  await invalidated.promise;
  expect(session.signal.aborted).toBe(true);
  expect(provider.isAuthenticated()).toBe(false);
  closing.resolve();
  expect(await result).toBeInstanceOf(AuthSessionChangedError);
  expect(await provider.getToken()).toBe(jwt('B'));
});

test('pending opens register ownership before token await and cannot resurrect a replaced session', async () => {
  const provider = new DefaultTokenProvider({ token: jwt('A') });
  const pending = createReplicaSession(provider).catch(error => error);
  provider.setToken(jwt('B'));
  expect(await pending).toBeInstanceOf(AuthSessionChangedError);
  expect((await createReplicaSession(provider)).subject).toBe('B');
});

test('tracked work is drained and resource errors remain observable through void setters', async () => {
  const provider = new DefaultTokenProvider({ token: jwt('A') });
  const session = await createReplicaSession(provider);
  const running = deferred(), entered = deferred();
  const work = session.track(async () => { entered.resolve(); await running.promise; }).catch(error => error);
  await entered.promise;
  const failure = new Error('storage close failed');
  session.register({ invalidate() {}, close: async () => { throw failure; } });
  provider.setToken(jwt('B'));
  let drained = false;
  const token = provider.getToken().catch(error => { drained = true; return error; });
  await Promise.resolve(); expect(drained).toBe(false);
  running.resolve();
  expect(await work).toBeInstanceOf(AuthSessionChangedError);
  expect(await token).toBe(failure);
  await expect(provider.refreshToken()).rejects.toBe(failure);
});

test('opaque REST tokens remain supported, while unsupported replica providers fail explicitly', async () => {
  const provider = new DefaultTokenProvider({ token: 'opaque' });
  expect(await provider.getToken()).toBe('opaque');
  await expect(createReplicaSession(provider)).rejects.toThrow('Malformed');
  await expect(createReplicaSession({ getSessionVersion: () => 1 } as never)).rejects.toThrow('lifecycle');
});

test('back-to-back setters retain intended access token while previous owners drain', async () => {
  const provider = new DefaultTokenProvider({ token: jwt('A') });
  const session = await createReplicaSession(provider);
  const closing = deferred();
  session.register({ invalidate() {}, close: () => closing.promise });
  provider.setToken(jwt('B')); provider.setRefreshToken('B-refresh');
  closing.resolve();
  expect(await provider.getToken()).toBe(jwt('B'));
  axios.post = (async (_url, body) => { expect(body as unknown).toEqual({ refresh_token: 'B-refresh' }); return { data: { access_token: jwt('B') } }; }) as typeof axios.post;
  await provider.refreshToken();
});


test('account-changing refresh inside tracked HTTP work cannot wait for its own drain', async () => {
  const provider = new DefaultTokenProvider({ token: jwt('A'), refreshToken: 'refresh' });
  const session = await createReplicaSession(provider);
  axios.post = (async () => ({ data: { access_token: jwt('B') } })) as typeof axios.post;
  await expect(session.track(() => provider.refreshToken())).rejects.toBeInstanceOf(AuthSessionChangedError);
  expect(await provider.getToken()).toBe(jwt('B'));
  expect(session.signal.aborted).toBe(true);
}, 1000);


test('a manually closing session remains an auth drain owner until resources close', async () => {
  const provider = new DefaultTokenProvider({ token: jwt('A') });
  const session = await createReplicaSession(provider);
  const gate = deferred();
  session.register({ invalidate() {}, close: () => gate.promise });
  const closing = session.close();
  provider.setToken(jwt('B'));
  let installed = false;
  const token = provider.getToken().then(value => { installed = true; return value; });
  await Promise.resolve(); expect(installed).toBe(false);
  gate.resolve(); await closing;
  expect(await token).toBe(jwt('B'));
});


for (const nextSubject of ['A', 'B']) {
  test(`pending token snapshot fences refresh to ${nextSubject} before the open continuation`, async () => {
    const provider = new DefaultTokenProvider({ token: jwt('A'), refreshToken: 'refresh' });
    const version = provider.getSessionVersion();
    const started = deferred();
    let finish!: (response: { data: { access_token: string } }) => void;
    const response = new Promise<{ data: { access_token: string } }>(resolve => { finish = resolve; });
    axios.post = (() => { started.resolve(); return response; }) as typeof axios.post;
    const refreshed = provider.refreshToken().catch(error => error);
    await started.promise;
    finish({ data: { access_token: jwt(nextSubject) } });
    const opening = createReplicaSession(provider).catch(error => error);
    const opened = await opening;
    const refreshResult = await refreshed;
    if (nextSubject === 'A') {
      expect(refreshResult).toBe(jwt('A'));
      expect(opened.subject).toBe('A');
      opened.assertCurrent();
      expect(provider.getSessionVersion()).toBe(version);
      await opened.close();
    } else {
      expect(opened).toBeInstanceOf(AuthSessionChangedError);
      expect(refreshResult).toBeInstanceOf(AuthSessionChangedError);
      expect(provider.getSessionVersion()).toBeGreaterThan(version);
    }
    expect(await provider.getToken()).toBe(jwt(nextSubject));
  }, 1000);
}
