import { describe, expect, it, vi } from 'vitest';
import { createSessionController, type SessionRuntime } from './controller';
import { AuthError, type AuthTransport, type ConsoleSession } from './transport';

const alice: ConsoleSession = { developer: { id: 'alice', username: 'Alice' }, expires_at: '2026-10-13T12:00:00Z', csrf_token: 'alice-csrf' };
const bob: ConsoleSession = { ...alice, developer: { id: 'bob', username: 'Bob' }, csrf_token: 'bob-csrf' };
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve(); };
const environment = () => {
  let visible = true;
  let onVisibility = () => {};
  let onChange = () => {};
  const timers = new Set<() => void>();
  const close = vi.fn();
  const notify = vi.fn();
  const removeVisibility = vi.fn();
  const runtime: SessionRuntime = {
    now: () => Date.parse('2026-10-12T12:00:00Z'),
    schedule: (callback) => { timers.add(callback); return () => { timers.delete(callback); }; },
    visible: () => visible,
    onVisibility: (callback) => { onVisibility = callback; return removeVisibility; },
    channel: (callback) => { onChange = callback; return { notify, close }; },
  };
  return { runtime, notify, close, timers, removeVisibility, visibility: (value = true) => { visible = value; onVisibility(); }, peer: () => onChange(), expire: () => { [...timers].forEach((timer) => timer()); } };
};
const transport = () => ({ me: vi.fn<AuthTransport['me']>().mockResolvedValue(alice), login: vi.fn<AuthTransport['login']>().mockResolvedValue(bob), logout: vi.fn<AuthTransport['logout']>().mockResolvedValue(undefined) });

describe('session authority controller', () => {
  it('restores, expires, and cleans up owned timers/events', async () => {
    const api = transport();
    const env = environment();
    const controller = createSessionController(api, env.runtime);
    const listener = vi.fn();
    const unsubscribe = controller.subscribe(listener);
    const disconnect = controller.connect();
    expect(controller.getSnapshot().status).toBe('restoring');
    await flush();
    expect(controller.getSnapshot()).toMatchObject({ status: 'authenticated', session: alice });
    api.me.mockRejectedValueOnce(new AuthError('unauthorized', 'me', { status: 401 }));
    env.expire();
    await flush();
    expect(controller.getSnapshot()).toMatchObject({ status: 'anonymous', session: null, notice: 'session-ended' });
    expect(listener).toHaveBeenCalled();
    unsubscribe();
    disconnect();
    expect(env.timers.size).toBe(0);
    expect(env.close).toHaveBeenCalledOnce();
    expect(env.removeVisibility).toHaveBeenCalledOnce();
  });

  it('distinguishes anonymous restoration from unavailable and permits retry', async () => {
    const api = transport();
    api.me.mockRejectedValueOnce(new AuthError('unavailable', 'me')).mockRejectedValueOnce(new AuthError('unauthorized', 'me'));
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect();
    await flush();
    expect(controller.getSnapshot()).toMatchObject({ status: 'unavailable', session: null });
    await controller.retry();
    expect(controller.getSnapshot()).toMatchObject({ status: 'anonymous', error: null, notice: null });
    disconnect();
  });

  it.each(['resolve', 'reject'] as const)('a delayed bootstrap %s cannot discard a newer login', async (outcome) => {
    const bootstrap = deferred<ConsoleSession>();
    const api = transport();
    api.me.mockReturnValueOnce(bootstrap.promise).mockResolvedValue(bob);
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect();
    const signal = api.me.mock.calls[0]![0];
    await controller.login('bob', 'password');
    expect(signal.aborted).toBe(true);
    if (outcome === 'resolve') bootstrap.resolve(alice);
    else bootstrap.reject(new AuthError('unauthorized', 'me'));
    await flush();
    expect(controller.getSnapshot()).toMatchObject({ status: 'authenticated', session: bob });
    disconnect();
  });

  it('a delayed pre-delete read cannot revive a completed logout', async () => {
    const old = deferred<ConsoleSession>();
    const api = transport();
    api.me.mockResolvedValueOnce(alice).mockReturnValueOnce(old.promise).mockRejectedValueOnce(new AuthError('unauthorized', 'me'));
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect();
    await flush();
    const read = controller.retry();
    await controller.logout();
    old.resolve(alice);
    await read;
    expect(controller.getSnapshot()).toMatchObject({ status: 'anonymous', session: null, notice: null });
    expect(api.logout).toHaveBeenCalledWith('alice-csrf', expect.any(AbortSignal));
    disconnect();
  });

  it.each(['login', 'logout'] as const)('queues all revalidation triggers behind pending %s', async (phase) => {
    const pending = deferred<ConsoleSession>();
    const deleting = deferred<void>();
    const api = transport();
    api.login.mockReturnValueOnce(pending.promise);
    api.logout.mockReturnValueOnce(deleting.promise);
    const env = environment();
    const controller = createSessionController(api, env.runtime);
    const disconnect = controller.connect();
    await flush();
    const mutation = phase === 'login' ? controller.login('bob', 'password') : controller.logout();
    const operationSignal = phase === 'login' ? api.login.mock.calls[0]![2] : api.logout.mock.calls[0]![1];
    env.visibility(); env.peer(); await controller.retry();
    expect(api.me).toHaveBeenCalledTimes(1);
    expect(operationSignal.aborted).toBe(false);
    expect(controller.getSnapshot().mutation).toBe(phase);
    if (phase === 'login') { api.me.mockResolvedValueOnce(bob); pending.resolve(bob); }
    else { api.me.mockRejectedValueOnce(new AuthError('unauthorized', 'me')); deleting.resolve(); }
    await mutation;
    expect(api.me).toHaveBeenCalledTimes(2);
    expect(controller.getSnapshot().status).toBe(phase === 'login' ? 'authenticated' : 'anonymous');
    expect(env.notify).toHaveBeenCalledOnce();
    disconnect();
  });

  it('prevents duplicate mutations and keeps confirmed credential denial form-owned', async () => {
    const pending = deferred<ConsoleSession>();
    const api = transport();
    api.login.mockReturnValueOnce(pending.promise);
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect(); await flush();
    const login = controller.login('bob', 'wrong');
    await expect(controller.login('bob', 'again')).rejects.toMatchObject({ category: 'busy' });
    pending.reject(new AuthError('unauthorized', 'login'));
    await expect(login).rejects.toMatchObject({ category: 'unauthorized' });
    expect(controller.getSnapshot()).toMatchObject({ status: 'authenticated', session: alice, mutation: null, error: null });
    disconnect();
  });

  it.each(['invalid-response', 'timeout', 'network', 'canceled'] as const)('reconciles %s login outcomes without replay', async (category) => {
    const api = transport();
    api.login.mockRejectedValueOnce(new AuthError(category, 'login'));
    api.me.mockRejectedValueOnce(new AuthError('unauthorized', 'me')).mockResolvedValueOnce(bob);
    const env = environment();
    const controller = createSessionController(api, env.runtime);
    const disconnect = controller.connect(); await flush();
    await controller.login('bob', 'password');
    expect(controller.getSnapshot()).toMatchObject({ status: 'authenticated', session: bob });
    expect(api.login).toHaveBeenCalledOnce();
    expect(env.notify).toHaveBeenCalledOnce();
    disconnect();
  });

  it('an uncertain logout retains only a blocked snapshot until authority recovers', async () => {
    const recovery = deferred<ConsoleSession>();
    const api = transport();
    api.logout.mockRejectedValueOnce(new AuthError('network', 'logout'));
    api.me.mockResolvedValueOnce(alice).mockReturnValueOnce(recovery.promise).mockRejectedValueOnce(new AuthError('unauthorized', 'me'));
    const env = environment();
    const controller = createSessionController(api, env.runtime);
    const disconnect = controller.connect(); await flush();
    const logout = controller.logout(); await flush();
    expect(controller.getSnapshot()).toMatchObject({ status: 'reconciling', session: alice, notice: 'logout-failed' });
    await expect(controller.logout()).rejects.toMatchObject({ category: 'busy' });
    recovery.reject(new AuthError('unavailable', 'me'));
    await logout;
    expect(controller.getSnapshot()).toMatchObject({ status: 'unavailable', session: alice, notice: 'logout-failed' });
    await expect(controller.logout()).rejects.toMatchObject({ category: 'busy' });
    await controller.retry();
    expect(controller.getSnapshot()).toMatchObject({ status: 'anonymous', session: null });
    expect(api.logout).toHaveBeenCalledOnce();
    expect(env.notify).toHaveBeenCalledOnce();
    disconnect();
  });

  it('failed logout with a live server session remains visible and retryable', async () => {
    const api = transport();
    api.logout.mockRejectedValueOnce(new AuthError('unavailable', 'logout'));
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect(); await flush();
    await controller.logout();
    expect(controller.getSnapshot()).toMatchObject({ status: 'authenticated', session: alice, notice: 'logout-failed', error: { category: 'unavailable' } });
    api.me.mockRejectedValueOnce(new AuthError('unauthorized', 'me'));
    await controller.logout();
    expect(controller.getSnapshot().status).toBe('anonymous');
    disconnect();
  });

  it('CSRF denial resynchronizes a shared cookie account without replaying logout', async () => {
    const api = transport();
    api.logout.mockRejectedValueOnce(new AuthError('forbidden', 'logout'));
    api.me.mockResolvedValueOnce(alice).mockResolvedValueOnce(bob);
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect(); await flush();
    await controller.logout();
    expect(controller.getSnapshot()).toMatchObject({ status: 'authenticated', session: bob, notice: 'logout-failed' });
    expect(api.logout).toHaveBeenCalledOnce();
    disconnect();
  });

  it('expired logout clears authority and anonymous logout dispatches nothing', async () => {
    const api = transport();
    api.logout.mockRejectedValueOnce(new AuthError('unauthorized', 'logout'));
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect(); await flush();
    await controller.logout(); await controller.logout();
    expect(controller.getSnapshot()).toMatchObject({ status: 'anonymous', session: null });
    expect(api.logout).toHaveBeenCalledOnce();
    disconnect();
  });

  it('refreshes peer changes only while visible, then refreshes on return', async () => {
    const api = transport(); const env = environment();
    const controller = createSessionController(api, env.runtime);
    const disconnect = controller.connect(); await flush();
    env.visibility(false); env.peer();
    expect(api.me).toHaveBeenCalledOnce();
    api.me.mockResolvedValueOnce(bob);
    env.visibility(); await flush();
    expect(controller.getSnapshot().session).toEqual(bob);
    api.me.mockRejectedValueOnce(new AuthError('unauthorized', 'me'));
    env.peer(); await flush();
    expect(controller.getSnapshot().status).toBe('anonymous');
    disconnect();
  });

  it('disconnect cancels pending mutation and reconnect restores rather than publishing its old result', async () => {
    const pending = deferred<ConsoleSession>();
    const api = transport(); api.login.mockReturnValueOnce(pending.promise);
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect(); await flush();
    const login = controller.login('bob', 'password');
    const signal = api.login.mock.calls[0]![2];
    disconnect(); expect(signal.aborted).toBe(true);
    const disconnectAgain = controller.connect();
    await flush(); pending.resolve(bob); await login;
    expect(controller.getSnapshot()).toMatchObject({ status: 'authenticated', session: alice });
    disconnectAgain(); await controller.retry();
    await expect(controller.login('bob', 'password')).rejects.toMatchObject({ category: 'busy' });
  });

  it('the newest ordinary read owns state and late canceled reads cannot publish', async () => {
    const old = deferred<ConsoleSession>();
    const api = transport(); api.me.mockReturnValueOnce(old.promise).mockResolvedValueOnce(bob);
    const controller = createSessionController(api, environment().runtime);
    const disconnect = controller.connect();
    const signal = api.me.mock.calls[0]![0];
    await controller.retry();
    old.reject(new AuthError('unauthorized', 'me'));
    await flush();
    expect(signal.aborted).toBe(true);
    expect(controller.getSnapshot().session).toEqual(bob);
    disconnect();
  });
});
