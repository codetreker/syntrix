import { describe, it, expect, mock, afterEach } from 'bun:test';
import { DefaultTokenProvider } from './provider';
import axios from 'axios';
import { AuthSessionChangedError } from '../../api/errors';

const originalPost = axios.post;

describe('DefaultTokenProvider', () => {
  afterEach(() => {
    axios.post = originalPost;
  });

  it('should call /signup endpoint and set tokens', async () => {
    const postMock = mock(async (url: string, body: any) => {
      expect(url).toBe('http://localhost:8080/auth/v1/signup');
      expect(body).toEqual({ username: 'alice', password: 'password123' });
      return { data: { access_token: 'at', refresh_token: 'rt', expires_in: 3600 } };
    }) as any;
    axios.post = postMock;

    const provider = new DefaultTokenProvider({}, 'http://localhost:8080');
    const resp = await provider.signup('alice', 'password123');

    expect(resp.access_token).toBe('at');
    expect(resp.refresh_token).toBe('rt');
    expect(await provider.getToken()).toBe('at');
  });

  it('should call /login endpoint and set tokens', async () => {
    const postMock = mock(async (url: string, body: any) => {
      expect(url).toBe('http://auth/auth/v1/login');
      expect(body).toEqual({ username: 'alice', password: 'pw' });
      return { data: { access_token: 'at', refresh_token: 'rt', expires_in: 3600 } };
    }) as any;
    axios.post = postMock;

    const provider = new DefaultTokenProvider({ refreshUrl: 'http://auth/auth/v1/refresh' });
    const resp = await provider.login('alice', 'pw');

    expect(resp.access_token).toBe('at');
    expect(resp.refresh_token).toBe('rt');
    expect(await provider.getToken()).toBe('at');
  });

  it('should call derived /logout endpoint when refresh token exists', async () => {
    const postMock = mock(async (url: string, body: any) => {
      expect(url).toBe('http://auth/auth/v1/logout');
      expect(body).toEqual({ refresh_token: 'rt' });
      return { data: {} };
    }) as any;
    axios.post = postMock;

    const provider = new DefaultTokenProvider({ refreshToken: 'rt', refreshUrl: 'http://auth/auth/v1/refresh' });
    await provider.logout();

    expect(postMock).toHaveBeenCalled();
    expect(await provider.getToken()).toBeNull();
  });

  it('should serialize refresh calls', async () => {
    let callCount = 0;
    axios.post = mock(async () => {
        callCount++;
        await new Promise(resolve => setTimeout(resolve, 50));
        return { data: { access_token: 'new-token', refresh_token: 'new-refresh' } };
    }) as any;

    const provider = new DefaultTokenProvider({
      token: 'old',
      refreshToken: 'refresh',
      refreshUrl: 'http://refresh',
    });

    const p1 = provider.refreshToken();
    const p2 = provider.refreshToken();

    const [t1, t2] = await Promise.all([p1, p2]);

    expect(t1).toBe('new-token');
    expect(t2).toBe('new-token');
    expect(callCount).toBe(1);
  });

  it('should bubble refresh errors', async () => {
    axios.post = mock(async () => {
        throw new Error('Refresh failed');
    }) as any;

    const provider = new DefaultTokenProvider({
      token: 'old',
      refreshToken: 'refresh',
      refreshUrl: 'http://refresh',
    });

    try {
        await provider.refreshToken();
        expect(true).toBe(false); // Should not reach here
    } catch (e: any) {
        expect(e.message).toBe('Refresh failed');
    }
  });
});


type PendingRequest = {
  url: string;
  body: unknown;
  resolve: (data: unknown) => void;
  reject: (error: Error) => void;
};

const controlRequests = () => {
  const requests: PendingRequest[] = [];
  axios.post = mock((url: string, body: unknown) => new Promise((resolve, reject) => {
    requests.push({ url, body, resolve: data => resolve({ data }), reject });
  })) as typeof axios.post;
  return requests;
};

const credentials = (account: string) => ({
  access_token: `${account}-access`,
  refresh_token: `${account}-refresh`,
  expires_in: 3600,
});

describe('authentication session ownership', () => {
  afterEach(() => {
    axios.post = originalPost;
  });

  for (const method of ['login', 'signup'] as const) {
    for (const outcome of ['success', 'failure'] as const) {
      it(`rejects an obsolete ${method} ${outcome} after a newer login`, async () => {
        const requests = controlRequests();
        const provider = new DefaultTokenProvider({ token: 'old', refreshToken: 'old-refresh' });
        const initialVersion = provider.getSessionVersion();
        const obsolete = provider[method]('A', 'password');
        const rejected = obsolete.catch(error => error);
        expect(provider.isAuthenticated()).toBe(false);
        const pendingVersion = provider.getSessionVersion();
        expect(pendingVersion).toBeGreaterThan(initialVersion);

        const current = provider.login('B', 'password');
        const currentPendingVersion = provider.getSessionVersion();
        requests[1].resolve(credentials('B'));
        await current;
        expect(provider.getSessionVersion()).toBeGreaterThan(currentPendingVersion);
        if (outcome === 'success') requests[0].resolve(credentials('A'));
        else requests[0].reject(new Error('A login failed'));
        expect(await rejected).toBeInstanceOf(AuthSessionChangedError);
        expect(await provider.getToken()).toBe('B-access');
      });
    }

    it(`keeps the session empty after the current ${method} fails`, async () => {
      const requests = controlRequests();
      const provider = new DefaultTokenProvider({ token: 'old', refreshToken: 'old-refresh' });
      const error = new Error('Authentication rejected');
      const pending = provider[method]('A', 'password');
      const rejected = pending.catch(error => error);
      const version = provider.getSessionVersion();
      requests[0].reject(error);
      expect(await rejected).toBe(error);
      expect(provider.getSessionVersion()).toBe(version);
      expect(await provider.getToken()).toBeNull();
      await expect(provider.refreshToken()).rejects.toThrow('No refresh token available');
    });
  }

  it('invalidates a pending login immediately on logout', async () => {
    const requests = controlRequests();
    const provider = new DefaultTokenProvider({});
    const login = provider.login('A', 'password');
    const rejected = login.catch(error => error);
    await provider.logout();
    expect(requests).toHaveLength(1);
    requests[0].resolve(credentials('A'));
    expect(await rejected).toBeInstanceOf(AuthSessionChangedError);
    expect(provider.isAuthenticated()).toBe(false);
  });

  for (const outcome of ['success', 'failure'] as const) {
    it(`preserves a newer login when an old logout returns ${outcome}`, async () => {
      const requests = controlRequests();
      const provider = new DefaultTokenProvider({ token: 'A-access', refreshToken: 'A-refresh' });
      const logout = provider.logout();
      const error = new Error('Remote revoke unavailable');
      const completion = logout.catch(error => error);
      expect(provider.isAuthenticated()).toBe(false);
      expect(requests[0].body).toEqual({ refresh_token: 'A-refresh' });
      const login = provider.login('B', 'password');
      requests[1].resolve(credentials('B'));
      await login;
      const version = provider.getSessionVersion();
      if (outcome === 'success') requests[0].resolve({});
      else requests[0].reject(error);
      expect(await completion).toBe(outcome === 'failure' ? error : undefined);
      expect(await provider.getToken()).toBe('B-access');
      expect(provider.getSessionVersion()).toBe(version);
    });
  }

  for (const outcome of ['success', 'failure'] as const) {
    it(`suppresses a stale refresh ${outcome} and its callbacks after logout`, async () => {
      const requests = controlRequests();
      const onTokenRefresh = mock();
      const onAuthError = mock();
      const provider = new DefaultTokenProvider({
        token: 'A-access', refreshToken: 'A-refresh', onTokenRefresh, onAuthError,
      });
      const refresh = provider.refreshToken();
      const rejected = refresh.catch(error => error);
      await Promise.resolve();
      const logout = provider.logout();
      requests[1].resolve({});
      await logout;
      if (outcome === 'success') requests[0].resolve(credentials('A'));
      else requests[0].reject(new Error('Refresh failed'));
      expect(await rejected).toBeInstanceOf(AuthSessionChangedError);
      expect(provider.isAuthenticated()).toBe(false);
      expect(onTokenRefresh).not.toHaveBeenCalled();
      expect(onAuthError).not.toHaveBeenCalled();
    });
  }

  it('does not clear a new session refresh when an obsolete refresh finishes', async () => {
    const requests = controlRequests();
    const provider = new DefaultTokenProvider({ token: 'A-access', refreshToken: 'A-refresh' });
    const old = provider.refreshToken();
    const rejected = old.catch(error => error);
    await Promise.resolve();
    const login = provider.login('B', 'password');
    requests[1].resolve(credentials('B'));
    await login;
    const current = provider.refreshToken();
    await Promise.resolve();
    expect(requests[2].body).toEqual({ refresh_token: 'B-refresh' });
    requests[0].resolve(credentials('A'));
    expect(await rejected).toBeInstanceOf(AuthSessionChangedError);
    expect(await provider.getToken()).toBe('B-access');
    const shared = provider.refreshToken();
    await Promise.resolve();
    expect(requests).toHaveLength(3);
    requests[2].resolve(credentials('B-new'));
    expect(await Promise.all([current, shared])).toEqual(['B-new-access', 'B-new-access']);
  });

  it('supports refresh-only credentials without changing session version', async () => {
    const requests = controlRequests();
    const provider = new DefaultTokenProvider({ refreshToken: 'A-refresh' });
    const version = provider.getSessionVersion();
    const refresh = provider.refreshToken();
    const shared = provider.refreshToken();
    await Promise.resolve();
    expect(requests).toHaveLength(1);
    requests[0].resolve({ access_token: 'A-access' });
    expect(await Promise.all([refresh, shared])).toEqual(['A-access', 'A-access']);
    expect(provider.getSessionVersion()).toBe(version);
    const again = provider.refreshToken();
    await Promise.resolve();
    expect(requests[1].body).toEqual({ refresh_token: 'A-refresh' });
    requests[1].resolve(credentials('A-new'));
    await again;
    expect(provider.getSessionVersion()).toBe(version);
  });

  it('replaces an access token without retaining the previous refresh credential', async () => {
    const requests = controlRequests();
    const provider = new DefaultTokenProvider({ token: 'A-access', refreshToken: 'A-refresh' });
    const originalVersion = provider.getSessionVersion();
    provider.setToken('B-access');
    expect(provider.getSessionVersion()).toBeGreaterThan(originalVersion);
    expect(await provider.getToken()).toBe('B-access');
    await expect(provider.refreshToken()).rejects.toThrow('No refresh token available');
    const accessVersion = provider.getSessionVersion();
    provider.setRefreshToken('B-refresh');
    expect(provider.getSessionVersion()).toBeGreaterThan(accessVersion);
    expect(await provider.getToken()).toBe('B-access');
    const refresh = provider.refreshToken();
    await Promise.resolve();
    expect(requests[0].body).toEqual({ refresh_token: 'B-refresh' });
    requests[0].resolve(credentials('B-new'));
    await refresh;
  });

  it('rejects refresh waiters when the success callback logs out', async () => {
    const requests = controlRequests();
    const onAuthError = mock();
    let logout!: Promise<void>;
    const provider = new DefaultTokenProvider({
      refreshToken: 'A-refresh', onAuthError,
      onTokenRefresh: () => { logout = provider.logout(); },
    });
    const refresh = provider.refreshToken();
    const shared = provider.refreshToken();
    const rejected = Promise.all([
      refresh.catch(error => error),
      shared.catch(error => error),
    ]);
    await Promise.resolve();
    requests[0].resolve(credentials('A'));
    expect(await rejected).toEqual([expect.any(AuthSessionChangedError), expect.any(AuthSessionChangedError)]);
    expect(provider.isAuthenticated()).toBe(false);
    expect(onAuthError).not.toHaveBeenCalled();
    expect(requests[1].body).toEqual({ refresh_token: 'A-refresh' });
    requests[1].resolve({});
    await logout;
  });

  it('rejects with session change when the error callback logs out', async () => {
    const requests = controlRequests();
    let logout!: Promise<void>;
    const failure = new Error('Refresh rejected');
    const onAuthError = mock(() => { logout = provider.logout(); });
    const provider = new DefaultTokenProvider({ refreshToken: 'A-refresh', onAuthError });
    const refresh = provider.refreshToken();
    const rejected = refresh.catch(error => error);
    await Promise.resolve();
    requests[0].reject(failure);
    expect(await rejected).toBeInstanceOf(AuthSessionChangedError);
    expect(onAuthError).toHaveBeenCalledWith(failure);
    requests[1].resolve({});
    await logout;
  });

  it('reports malformed current refresh responses without altering credentials', async () => {
    const requests = controlRequests();
    const onAuthError = mock();
    const provider = new DefaultTokenProvider({ token: 'A', refreshToken: 'A-refresh', onAuthError });
    const refresh = provider.refreshToken();
    const rejected = refresh.catch(error => error);
    await Promise.resolve();
    requests[0].resolve({ refresh_token: 'invalid' });
    expect(await rejected).toEqual(new Error('Invalid refresh response: missing token'));
    expect(await provider.getToken()).toBe('A');
    expect(onAuthError).toHaveBeenCalledTimes(1);
  });

  it('does not send a deferred refresh after a synchronous session replacement', async () => {
    const requests = controlRequests();
    const provider = new DefaultTokenProvider({ refreshToken: 'A-refresh' });
    const refresh = provider.refreshToken();
    const rejected = refresh.catch(error => error);
    provider.setToken('B-access');
    expect(await rejected).toBeInstanceOf(AuthSessionChangedError);
    expect(requests).toHaveLength(0);
  });
});
