import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthError, createAuthTransport, type ConsoleSession } from './transport';

const session: ConsoleSession = { developer: { id: 'alice', username: 'Alice' }, expires_at: '2026-10-13T12:00:00Z', csrf_token: 'csrf' };
const requestId = '0123456789abcdef0123456789abcdef';
const signal = () => new AbortController().signal;

beforeEach(() => { vi.spyOn(console, 'error').mockImplementation(() => {}); });
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });

describe('authentication transport', () => {
  it('uses same-origin cookies and exact login fields without modifying passwords', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(session, { headers: { 'X-Request-ID': requestId } }));
    expect(await createAuthTransport({ fetch }).login('Alice', ' exact password ', signal())).toEqual(session);
    const [path, options] = fetch.mock.calls[0]!;
    expect(path).toBe('/auth/v1/login');
    expect(options).toMatchObject({ method: 'POST', credentials: 'same-origin', body: '{"username":"Alice","password":" exact password "}' });
    expect(options!.headers).toEqual({ Accept: 'application/json', 'Content-Type': 'application/json' });
    expect(options!.signal).toBeInstanceOf(AbortSignal);
    expect(JSON.stringify(options!.headers)).not.toContain('Origin');
  });

  it('restores authority from me and handles logout 204 without decoding JSON', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValueOnce(Response.json(session)).mockResolvedValueOnce(new Response(null, { status: 204 }));
    const transport = createAuthTransport({ fetch });
    expect(await transport.me(signal())).toEqual(session);
    expect(fetch.mock.calls[0]).toMatchObject(['/auth/v1/me', { method: 'GET', credentials: 'same-origin' }]);
    await expect(transport.logout('csrf-value', signal())).resolves.toBeUndefined();
    expect(fetch.mock.calls[1]).toMatchObject(['/auth/v1/logout', { method: 'POST', headers: { 'X-CSRF-Token': 'csrf-value' } }]);
  });

  it.each([[401, 'unauthorized'], [403, 'forbidden'], [400, 'invalid-request'], [413, 'invalid-request'], [415, 'invalid-request'], [404, 'not-found'], [409, 'conflict'], [503, 'unavailable'], [500, 'unavailable']] as const)(
    'preserves %i while refusing to expose non-JSON backend bodies', async (status, category) => {
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(new Response('private password and postgres DSN', { status, headers: { 'X-Request-ID': requestId } }));
      try { await createAuthTransport({ fetch }).me(signal()); throw new Error('expected failure'); }
      catch (error) {
        expect(error).toMatchObject({ category, status, phase: 'me', requestId });
        expect(String(error)).not.toContain('private');
      }
    },
  );

  it('refuses invalid request IDs and a wrong success status', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(new Response(null, { status: 202, headers: { 'X-Request-ID': 'secret-or-invalid' } }));
    await expect(createAuthTransport({ fetch }).logout('csrf', signal())).rejects.toMatchObject({ category: 'invalid-response', status: 202, requestId: undefined });
  });

  it.each([null, [], {}, { ...session, developer: null }, { ...session, developer: { id: '', username: 'Alice' } }, { ...session, developer: { id: 'id', username: '' } }, { ...session, expires_at: 'invalid' }, { ...session, csrf_token: '' }])(
    'rejects incomplete authority from a success body %j', async (body) => {
      const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(body));
      await expect(createAuthTransport({ fetch }).me(signal())).rejects.toMatchObject({ category: 'invalid-response', status: 200 });
    },
  );

  it('classifies truncated successful login JSON as an unknown response', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(new Response('{"developer":', { status: 200, headers: { 'X-Request-ID': requestId } }));
    await expect(createAuthTransport({ fetch }).login('alice', 'password', signal())).rejects.toMatchObject({ category: 'invalid-response', requestId });
  });

  it('preserves network causes without exposing them in its own message', async () => {
    const cause = new TypeError('private credential');
    const fetch = vi.fn<typeof globalThis.fetch>().mockRejectedValue(cause);
    const failure = await createAuthTransport({ fetch }).me(signal()).catch((error: unknown) => error);
    expect(failure).toBeInstanceOf(AuthError);
    expect(failure).toMatchObject({ category: 'network', cause });
    expect(String(failure)).not.toContain('private');
    expect(console.error).toHaveBeenCalledWith('Console authentication request failed', {
      component: 'auth-transport', phase: 'me', method: 'GET', path: '/auth/v1/me', error_category: 'network',
      http_status: undefined, request_id: undefined, cause_type: 'TypeError',
    });
    expect(JSON.stringify(vi.mocked(console.error).mock.calls)).not.toContain('private credential');
  });

  it('does not dispatch a pre-canceled request', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>();
    const controller = new AbortController();
    controller.abort();
    await expect(createAuthTransport({ fetch }).me(controller.signal)).rejects.toMatchObject({ category: 'canceled' });
    expect(fetch).not.toHaveBeenCalled();
  });

  it('bounds requests and removes parent cancellation listeners', async () => {
    vi.useFakeTimers();
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation((_url, options) => new Promise((_resolve, reject) => options!.signal!.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })));
    const parent = new AbortController();
    const remove = vi.spyOn(parent.signal, 'removeEventListener');
    const pending = createAuthTransport({ fetch, timeoutMs: 30 }).me(parent.signal);
    const assertion = expect(pending).rejects.toMatchObject({ category: 'timeout' });
    await vi.advanceTimersByTimeAsync(30);
    await assertion;
    expect(remove).toHaveBeenCalledWith('abort', expect.any(Function));
    expect(vi.getTimerCount()).toBe(0);
  });

  it('classifies caller cancellation separately from timeout', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation((_url, options) => new Promise((_resolve, reject) => options!.signal!.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })));
    const parent = new AbortController();
    const pending = createAuthTransport({ fetch }).me(parent.signal);
    parent.abort();
    await expect(pending).rejects.toMatchObject({ category: 'canceled' });
  });

  it('keeps the deadline active while reading a success body', async () => {
    vi.useFakeTimers();
    const fetch = vi.fn<typeof globalThis.fetch>().mockImplementation(async (_url, options) => ({
      status: 200, ok: true, headers: new Headers({ 'X-Request-ID': requestId }),
      json: () => new Promise((_resolve, reject) => options!.signal!.addEventListener('abort', () => reject(new Error('body canceled')), { once: true })),
    }) as Response);
    const pending = createAuthTransport({ fetch, timeoutMs: 30 }).me(signal());
    const assertion = expect(pending).rejects.toMatchObject({ category: 'timeout', requestId });
    await vi.advanceTimersByTimeAsync(30);
    await assertion;
  });

  it('requires a positive timeout', () => {
    expect(() => createAuthTransport({ timeoutMs: 0 })).toThrow(RangeError);
  });
});
