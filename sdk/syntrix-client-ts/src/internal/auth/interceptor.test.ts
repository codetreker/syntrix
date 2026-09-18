import { describe, it, expect, mock, spyOn } from 'bun:test';
import axios, { AxiosError, AxiosHeaders, InternalAxiosRequestConfig } from 'axios';
import { AuthSessionChangedError, SyntrixError } from '../../api/errors';
import { setupAuthInterceptor } from './interceptor';
import { DefaultTokenProvider } from './provider';
import { TokenProvider } from './types';
import { createLocalSession } from '../local/session';

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};
const response = (config: InternalAxiosRequestConfig, status = 200) => ({
  data: 'result', status, statusText: String(status), headers: {}, config,
});
const authFailure = (config: InternalAxiosRequestConfig, status = 401) =>
  new AxiosError('Authentication failed', 'ERR_BAD_REQUEST', config, undefined, response(config, status));
const fixture = () => {
  let version = 0;
  let token: string | null = 'A';
  const provider: TokenProvider = {
    getSessionVersion: () => version,
    getToken: mock(async () => token),
    refreshToken: mock(async () => { token = 'A-refreshed'; return token; }),
    setToken: value => { version++; token = value; },
    setRefreshToken: () => { version++; },
  };
  return { provider };
};

describe('AuthInterceptor session ownership', () => {
  for (const withSignal of [false, true]) {
    it(`drains an owned request when credentials change before the interceptor runs, signal=${withSignal}`, async () => {
      const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
      const provider = new DefaultTokenProvider({ token: jwt('A') });
      const session = await createLocalSession(provider);
      const adapter = mock(async (config: InternalAxiosRequestConfig) => response(config));
      const instance = axios.create({ adapter });
      setupAuthInterceptor(instance, provider);
      const result = session.track(() => {
        const request = instance.get('/document', withSignal ? { signal: session.signal } : {});
        provider.setToken(jwt('B'));
        return request;
      }).catch(error => error);
      expect(await result).toBeInstanceOf(AuthSessionChangedError);
      expect(await provider.getToken()).toBe(jwt('B'));
      expect(adapter).not.toHaveBeenCalled();
      await session.close();
    }, 1_000);
  }

  it('captures ownership before other asynchronous request interceptors and keeps explicit scopes', async () => {
    const { provider } = fixture();
    const entered = deferred<void>();
    const release = deferred<void>();
    const adapter = mock(async (config: InternalAxiosRequestConfig) => response(config));
    const instance = axios.create({ adapter });
    setupAuthInterceptor(instance, provider);
    instance.interceptors.request.use(async config => { entered.resolve(); await release.promise; return config; });
    const request = instance.get('/document').catch(error => error);
    await entered.promise;
    provider.setToken('B');
    release.resolve();
    expect(await request).toBeInstanceOf(AuthSessionChangedError);
    expect(await instance.get('/document', { _syntrixAuthSessionVersion: 0 } as any).catch(error => error))
      .toBeInstanceOf(AuthSessionChangedError);
    expect(provider.getToken).not.toHaveBeenCalled();
    expect(adapter).not.toHaveBeenCalled();
  });

  for (const credential of ['token', 'refresh'] as const) {
    it(`cancels a suspended ${credential} read without waiting for the provider`, async () => {
      const { provider } = fixture();
      const entered = deferred<void>();
      const pending = deferred<string>();
      const read = mock(async () => { entered.resolve(); return pending.promise; });
      if (credential === 'token') provider.getToken = read;
      else provider.refreshToken = read;
      const controller = new AbortController();
      const add = spyOn(controller.signal, 'addEventListener');
      const remove = spyOn(controller.signal, 'removeEventListener');
      const adapter = mock(async (config: InternalAxiosRequestConfig) => { throw authFailure(config); });
      const instance = axios.create({ adapter });
      setupAuthInterceptor(instance, provider);
      const request = instance.get('/document', { signal: controller.signal }).catch(error => error);
      await entered.promise;
      controller.abort();
      const error = await request;
      expect(axios.isCancel(error)).toBe(true);
      expect(error.cause).toBe(controller.signal.reason);
      expect(adapter).toHaveBeenCalledTimes(credential === 'token' ? 0 : 1);
      expect(remove.mock.calls).toEqual(add.mock.calls);
      pending.reject(new Error('late credential failure'));
      await Promise.resolve();
    }, 1_000);
  }

  it('rejects an already canceled request before reading credentials', async () => {
    const { provider } = fixture();
    const controller = new AbortController();
    controller.abort();
    const instance = axios.create();
    setupAuthInterceptor(instance, provider);
    expect(axios.isCancel(await instance.get('/document', { signal: controller.signal }).catch(error => error))).toBe(true);
    expect(provider.getToken).not.toHaveBeenCalled();
  });

  it('preserves cancellation provenance from a dispatched Axios adapter', async () => {
    const { provider } = fixture();
    const entered = deferred<void>();
    const controller = new AbortController();
    const adapter = mock((config: InternalAxiosRequestConfig) => new Promise<never>((_resolve, reject) => {
      config.signal!.addEventListener!('abort', () => reject(new axios.CanceledError('adapter canceled', config)), { once: true });
      entered.resolve();
    }));
    const instance = axios.create({ adapter });
    setupAuthInterceptor(instance, provider);
    const request = instance.get('/document', { signal: controller.signal }).catch(error => error);
    await entered.promise;
    const reason = new AuthSessionChangedError();
    controller.abort(reason);
    const error = await request;
    expect(axios.isCancel(error)).toBe(true);
    expect(error.message).toBe('adapter canceled');
    expect(error.cause).toBe(reason);
    expect(adapter).toHaveBeenCalledTimes(1);
  }, 1_000);

  it('supports CancelToken while credentials are pending and detaches its listener', async () => {
    const { provider } = fixture();
    const entered = deferred<void>();
    const pending = deferred<string>();
    provider.getToken = async () => { entered.resolve(); return pending.promise; };
    const cancellation = axios.CancelToken.source();
    const subscribe = spyOn(cancellation.token, 'subscribe');
    const unsubscribe = spyOn(cancellation.token, 'unsubscribe');
    const instance = axios.create();
    setupAuthInterceptor(instance, provider);
    const request = instance.get('/document', { cancelToken: cancellation.token }).catch(error => error);
    await entered.promise;
    cancellation.cancel('stop waiting');
    expect(await request).toBe(cancellation.token.reason);
    expect(unsubscribe.mock.calls).toEqual(subscribe.mock.calls);
    pending.resolve('late');
  }, 1_000);

  it('does not release a failed cleanup barrier when a waiting request is canceled', async () => {
    const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
    const provider = new DefaultTokenProvider({ token: jwt('A') });
    const session = await createLocalSession(provider);
    const cleanup = deferred<void>();
    session.register({ invalidate: () => {}, close: () => cleanup.promise });
    provider.setToken(jwt('B'));
    const entered = deferred<void>();
    const getToken = provider.getToken.bind(provider);
    provider.getToken = () => { entered.resolve(); return getToken(); };
    const controller = new AbortController();
    const adapter = mock(async (config: InternalAxiosRequestConfig) => response(config));
    const instance = axios.create({ adapter });
    setupAuthInterceptor(instance, provider);
    const request = instance.get('/document', { signal: controller.signal }).catch(error => error);
    await entered.promise;
    controller.abort();
    expect(axios.isCancel(await request)).toBe(true);
    expect(adapter).not.toHaveBeenCalled();
    const failure = new Error('storage cleanup failed');
    cleanup.reject(failure);
    await expect(getToken()).rejects.toBe(failure);
    expect(provider.isAuthenticated()).toBe(false);
    await expect(session.close()).rejects.toBe(failure);
  }, 1_000);

  it('preserves the request session through a real Axios retry', async () => {
    const { provider } = fixture();
    const received: { token: unknown; version: unknown }[] = [];
    const instance = axios.create({ adapter: async config => {
      received.push({ token: config.headers.get('Authorization'), version: (config as any)._syntrixAuthSessionVersion });
      if (received.length === 1) throw authFailure(config);
      return response(config);
    } });
    setupAuthInterceptor(instance, provider);
    expect((await instance.get('/document')).data).toBe('result');
    expect(received.map(config => config.token)).toEqual(['Bearer A', 'Bearer A-refreshed']);
    expect(received.map(config => config.version)).toEqual([0, 0]);
    expect(provider.refreshToken).toHaveBeenCalledTimes(1);
  });

  it('removes stale Authorization when there is no access token', async () => {
    const provider = new DefaultTokenProvider({});
    const adapter = mock(async (config: InternalAxiosRequestConfig) => {
      expect(config.headers.has('Authorization')).toBe(false);
      return response(config);
    });
    const instance = axios.create({ adapter, headers: { authorization: 'Bearer stale' } });
    setupAuthInterceptor(instance, provider);
    await instance.get('/public');
    expect(adapter).toHaveBeenCalledTimes(1);
  });

  for (const status of [401, 403]) {
    for (const alreadyRetried of [false, true]) {
      it(`rejects an old ${status} after replacement, retry=${alreadyRetried}`, async () => {
        const { provider } = fixture();
        const entered = deferred<void>();
        const finish = deferred<void>();
        const adapter = mock(async (config: InternalAxiosRequestConfig) => {
          entered.resolve();
          await finish.promise;
          throw authFailure(config, status);
        });
        const instance = axios.create({ adapter });
        setupAuthInterceptor(instance, provider);
        const result = instance.get('/document', { _retry: alreadyRetried } as any).catch(error => error);
        await entered.promise;
        provider.setToken('B');
        finish.resolve();
        expect(await result).toBeInstanceOf(AuthSessionChangedError);
        expect(provider.refreshToken).not.toHaveBeenCalled();
        expect(adapter).toHaveBeenCalledTimes(1);
      });
    }
  }

  for (const rejectToken of [false, true]) {
    it(`checks session after a suspended token read, rejection=${rejectToken}`, async () => {
      const { provider } = fixture();
      const entered = deferred<void>();
      const token = deferred<string>();
      provider.getToken = async () => { entered.resolve(); return token.promise; };
      const adapter = mock(async (config: InternalAxiosRequestConfig) => response(config));
      const instance = axios.create({ adapter });
      setupAuthInterceptor(instance, provider);
      const result = instance.get('/document').catch(error => error);
      await entered.promise;
      provider.setToken('B');
      if (rejectToken) token.reject(new Error('old token read failed'));
      else token.resolve('A');
      expect(await result).toBeInstanceOf(AuthSessionChangedError);
      expect(adapter).not.toHaveBeenCalled();
    });
  }

  for (const rejectRefresh of [false, true]) {
    it(`rejects refresh from an invalidated request, rejection=${rejectRefresh}`, async () => {
      const { provider } = fixture();
      const entered = deferred<void>();
      const refresh = deferred<string>();
      provider.refreshToken = mock(async () => { entered.resolve(); return refresh.promise; });
      const adapter = mock(async (config: InternalAxiosRequestConfig) => { throw authFailure(config); });
      const instance = axios.create({ adapter });
      setupAuthInterceptor(instance, provider);
      const result = instance.get('/document').catch(error => error);
      await entered.promise;
      provider.setToken('B');
      if (rejectRefresh) refresh.reject(new Error('old refresh failed'));
      else refresh.resolve('A-refreshed');
      expect(await result).toBeInstanceOf(AuthSessionChangedError);
      expect(adapter).toHaveBeenCalledTimes(1);
    });
  }

  it('does not rebind the config when the session changes before retry admission', async () => {
    const { provider } = fixture();
    const adapter = mock(async (config: InternalAxiosRequestConfig) => { throw authFailure(config); });
    const instance = axios.create({ adapter });
    setupAuthInterceptor(instance, provider);
    // Axios request interceptors run in reverse registration order. Change the
    // session just before the retry re-enters our authentication interceptor.
    instance.interceptors.request.use(config => {
      if ((config as any)._retry) provider.setToken('B');
      return config;
    });
    const result = await instance.get('/document').catch(error => error);
    expect(result).toBeInstanceOf(AuthSessionChangedError);
    expect(provider.refreshToken).toHaveBeenCalledTimes(1);
    expect(adapter).toHaveBeenCalledTimes(1);
  });

  it('rejects a request admitted during pending login after credentials are installed', async () => {
    const originalPost = axios.post;
    const login = deferred<any>();
    const entered = deferred<void>();
    const finish = deferred<void>();
    const provider = new DefaultTokenProvider({ token: 'A', refreshToken: 'A-refresh' });
    const refresh = mock(provider.refreshToken.bind(provider));
    provider.refreshToken = refresh;
    axios.post = mock(async () => login.promise) as any;
    try {
      const signingIn = provider.login('B', 'password');
      const adapter = mock(async (config: InternalAxiosRequestConfig) => {
        expect(config.headers.has('Authorization')).toBe(false);
        entered.resolve();
        await finish.promise;
        throw authFailure(config);
      });
      const instance = axios.create({ adapter });
      setupAuthInterceptor(instance, provider);
      const result = instance.get('/document').catch(error => error);
      await entered.promise;
      login.resolve({ data: { access_token: 'B', refresh_token: 'B-refresh', expires_in: 60 } });
      await signingIn;
      finish.resolve();
      expect(await result).toBeInstanceOf(AuthSessionChangedError);
      expect(refresh).not.toHaveBeenCalled();
      expect(adapter).toHaveBeenCalledTimes(1);
    } finally {
      axios.post = originalPost;
      finish.resolve();
    }
  });

  it('retains the existing single-retry rule within the same session', async () => {
    const { provider } = fixture();
    const adapter = mock(async (config: InternalAxiosRequestConfig) => { throw authFailure(config); });
    const instance = axios.create({ adapter });
    setupAuthInterceptor(instance, provider);
    expect(await instance.get('/document').catch(error => error)).toBeInstanceOf(AxiosError);
    expect(adapter).toHaveBeenCalledTimes(2);
    expect(provider.refreshToken).toHaveBeenCalledTimes(1);
  });

  it('preserves a session invalidation error from a custom provider', async () => {
    const { provider } = fixture();
    const expected = new AuthSessionChangedError();
    provider.refreshToken = async () => { throw expected; };
    const instance = axios.create({ adapter: async config => { throw authFailure(config); } });
    setupAuthInterceptor(instance, provider);
    expect(await instance.get('/document').catch(error => error)).toBe(expected);
  });

  it('preserves non-authentication response handling', async () => {
    const { provider } = fixture();
    const instance = axios.create({ adapter: async config => {
      throw new AxiosError('Rate limited', 'ERR_BAD_REQUEST', config, undefined, {
        ...response(config, 429), headers: new AxiosHeaders({ 'retry-after': '3' }),
      });
    } });
    setupAuthInterceptor(instance, provider);
    const error = await instance.get('/document').catch(error => error);
    expect(error).toBeInstanceOf(SyntrixError);
    expect(error.retryAfter).toBe(3);
    expect(provider.refreshToken).not.toHaveBeenCalled();
  });
});
