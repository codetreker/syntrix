import { describe, it, expect, mock, afterEach, beforeEach } from 'bun:test';
import { SyntrixClient } from './syntrix-client';
import axios from 'axios';

const originalPost = axios.post;

describe('SyntrixClient', () => {
  afterEach(() => {
    axios.post = originalPost;
  });

  it('should create collection reference', () => {
    const client = new SyntrixClient('http://localhost', { database: 'test-db' });
    const col = client.collection('users');
    expect(col.path).toBe('users');
  });

  it('should create doc reference from path', () => {
    const client = new SyntrixClient('http://localhost', { database: 'test-db' });
    const doc = client.doc('users/123');
    expect(doc.path).toBe('users/123');
    expect(doc.id).toBe('123');
  });

  it('should return database name', () => {
    const client = new SyntrixClient('http://localhost', { database: 'my-database' });
    expect(client.getDatabase()).toBe('my-database');
  });

  describe('Auth methods', () => {
    let client: SyntrixClient;
    let postMock: ReturnType<typeof mock>;

    beforeEach(() => {
      postMock = mock(async () => ({
        data: { access_token: 'test-token', refresh_token: 'test-refresh', expires_in: 3600 }
      })) as any;
      axios.post = postMock;
      client = new SyntrixClient('http://localhost', { database: 'test-db' });
    });

    it('should call signup', async () => {
      const result = await client.signup('testuser', 'testpass');
      expect(postMock).toHaveBeenCalledWith(
        'http://localhost/auth/v1/signup',
        { username: 'testuser', password: 'testpass' }
      );
      expect(result.access_token).toBe('test-token');
    });

    it('should call login', async () => {
      const result = await client.login('testuser', 'testpass');
      expect(postMock).toHaveBeenCalledWith(
        'http://localhost/auth/v1/login',
        { username: 'testuser', password: 'testpass' }
      );
      expect(result.access_token).toBe('test-token');
    });

    it('should call logout', async () => {
      // First login to get refresh token
      await client.login('testuser', 'testpass');

      // Then logout
      await client.logout();
      // Should have been called twice: once for login, once for logout
      expect(postMock.mock.calls.length).toBe(2);
      expect(postMock.mock.calls[1]).toEqual([
        'http://localhost/auth/v1/logout',
        { refresh_token: 'test-refresh' }
      ]);
    });

    it('should check isAuthenticated', async () => {
      expect(client.isAuthenticated()).toBe(false);
      await client.login('testuser', 'testpass');
      expect(client.isAuthenticated()).toBe(true);
    });
  });

  describe('SSE lifecycle', () => {
    for (const operation of ['login', 'signup', 'logout'] as const) {
      it(`invalidates credentials and detaches SSE before ${operation} teardown`, async () => {
        let finish!: (value: any) => void;
        axios.post = mock(() => new Promise(resolve => { finish = resolve; })) as any;
        const client = new SyntrixClient('http://localhost', {
          database: 'test-db', auth: { token: 'A', refreshToken: 'A-refresh' },
        });
        const oldSSE = client.realtimeSSE();
        let replacement!: ReturnType<SyntrixClient['realtimeSSE']>;
        const disconnect = mock(() => {
          expect(client.isAuthenticated()).toBe(false);
          replacement = client.realtimeSSE();
          expect(replacement).not.toBe(oldSSE);
        });
        oldSSE.disconnect = disconnect;
        const pending = operation === 'logout' ? client.logout() : client[operation]('B', 'password');
        expect(disconnect).toHaveBeenCalledTimes(1);
        expect(client.realtimeSSE()).toBe(replacement);
        finish({ data: { access_token: 'B', refresh_token: 'B-refresh', expires_in: 60 } });
        await pending;
        expect(client.isAuthenticated()).toBe(operation !== 'logout');
        client.realtimeSSE().disconnect();
      });
    }

    it('propagates remote logout failure after clearing owned SSE resources', async () => {
      const error = new Error('revocation unavailable');
      axios.post = mock(async () => { throw error; }) as any;
      const client = new SyntrixClient('http://localhost', {
        database: 'test-db', auth: { token: 'A', refreshToken: 'A-refresh' },
      });
      const oldSSE = client.realtimeSSE();
      const disconnect = mock(); oldSSE.disconnect = disconnect;
      const result = client.logout().catch(failure => failure);
      expect(client.isAuthenticated()).toBe(false);
      expect(client.realtimeSSE()).not.toBe(oldSSE);
      expect(disconnect).toHaveBeenCalledTimes(1);
      expect(await result).toBe(error);
      client.realtimeSSE().disconnect();
    });

    it('reuses the SSE client without exposing a public WebSocket client', () => {
      const client = new SyntrixClient('http://localhost', { database: 'test-db' });
      const sse = client.realtimeSSE();
      expect(sse).toBeDefined();
      expect(client.realtimeSSE()).toBe(sse);
      expect('realtime' in client).toBe(false);
      expect('subscribe' in client).toBe(false);
      expect(typeof client.pull).toBe('function');
    });
  });
});
