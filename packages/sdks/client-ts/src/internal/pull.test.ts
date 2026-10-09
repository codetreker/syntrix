import { afterEach, describe, expect, it } from 'bun:test';
import { SyntrixClient } from '../clients/syntrix-client';
import { AuthSessionChangedError, ErrorCodes, SyntrixError } from '../api/errors';
import { encodeQueryValue } from '../api/value';
import { decodePullPage } from './pull';

const live = {
  id: 'alice', collection: 'users', version: 9007199254740993n,
  createdAt: 1n, updatedAt: 2n, name: 'Alice', nested: { count: 9007199254740995n },
};
const page = (documents: unknown[] = [live], checkpoint = 'opaque-source-position', caughtUp = false) => ({
  documents: documents.map(encodeQueryValue), checkpoint, caughtUp,
});

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(res => { resolve = res; });
  return { promise, resolve };
};

describe('manual replication Pull', () => {
  let server: ReturnType<typeof Bun.serve> | undefined;
  afterEach(() => { server?.stop(true); server = undefined; });

  const serve = (fetch: (request: Request) => Response | Promise<Response>) => {
    server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch });
    return new SyntrixClient(`${server.url.origin}/prefix`, {
      database: 'db-id', auth: { token: 'access-token', database: 'unrelated-auth-scope' },
    });
  };

  it('uses the configured database and authenticated replication route while retaining URL prefixes', async () => {
    const bodies: unknown[] = [];
    const client = serve(async request => {
      expect(request.method).toBe('POST');
      expect(new URL(request.url).pathname).toBe('/prefix/replication/v1/databases/db-id/pull');
      expect(request.headers.get('Authorization')).toBe('Bearer access-token');
      bodies.push(await request.json());
      return Response.json(page());
    });
    const first = await client.pull<{ name: string; nested: { count: bigint } }>('users');
    expect(first).toEqual({ documents: [live], checkpoint: 'opaque-source-position', caughtUp: false });
    await client.pull('users', { checkpoint: first.checkpoint, limit: 7 });
    expect(bodies).toEqual([
      { collection: 'users', checkpoint: null, limit: 100 },
      { collection: 'users', checkpoint: 'opaque-source-position', limit: 7 },
    ]);
  });

  it('retains minimal deletions and empty progress pages without inventing metadata', async () => {
    const tombstone = { id: 'alice', collection: 'users', deleted: true as const };
    const client = serve(async request => {
      const body = await request.json() as { checkpoint: string | null };
      return Response.json(body.checkpoint === null ? page([tombstone], 'next') : page([], 'later', false));
    });
    const first = await client.pull('users', { checkpoint: null });
    expect(first.documents).toEqual([tombstone]);
    expect(Object.keys(first.documents[0]).sort()).toEqual(['collection', 'deleted', 'id']);
    expect(await client.pull('users', { checkpoint: first.checkpoint })).toEqual({
      documents: [], checkpoint: 'later', caughtUp: false,
    });
  });

  it('accepts a legal page larger than 4 MiB', async () => {
    const document = { ...live, text: 'x'.repeat(5 * 1024 * 1024) };
    const client = serve(() => Response.json(page([document], 'next', true)));
    expect((await client.pull<{ text: string }>('users')).documents).toEqual([document]);
  });

  it('rejects a successful old-session page after logout', async () => {
    const admitted = deferred<void>();
    const release = deferred<void>();
    const client = serve(async () => {
      admitted.resolve();
      await release.promise;
      return Response.json(page());
    });
    const pending = client.pull('users');
    const outcome = pending.catch(error => error);
    await admitted.promise;
    await client.logout();
    release.resolve();
    expect(await outcome).toBeInstanceOf(AuthSessionChangedError);
  });

  it('binds the request before an immediate session change can schedule new credentials', async () => {
    let calls = 0;
    const client = serve(() => { calls++; return Response.json(page()); });
    const pending = client.pull('users').catch(error => error);
    await client.logout();
    expect(await pending).toBeInstanceOf(AuthSessionChangedError);
    expect(calls).toBe(0);
  });

  it('propagates resync errors without exposing or advancing a checkpoint', async () => {
    const client = serve(() => Response.json({ code: 'RESYNC_REQUIRED', message: 'History expired' }, { status: 409 }));
    const error = await client.pull('users', { checkpoint: 'old' }).catch(error => error);
    expect(error).toBeInstanceOf(SyntrixError);
    expect(error.code).toBe(ErrorCodes.RESYNC_REQUIRED);
    expect(error.status).toBe(409);
  });

  it('accepts a same-session token refresh and retries with the original checkpoint', async () => {
    const bodies: unknown[] = [];
    serve(async request => {
      if (new URL(request.url).pathname.endsWith('/auth/v1/refresh')) {
        expect(await request.json()).toEqual({ refresh_token: 'refresh-token' });
        return Response.json({ access_token: 'fresh-token', refresh_token: 'next-refresh' });
      }
      bodies.push(await request.json());
      if (request.headers.get('Authorization') === 'Bearer expired-token') {
        return Response.json({ code: 'UNAUTHORIZED', message: 'Expired' }, { status: 401 });
      }
      expect(request.headers.get('Authorization')).toBe('Bearer fresh-token');
      return Response.json(page([], 'next', true));
    });
    const client = new SyntrixClient(`${server!.url.origin}/prefix`, {
      database: 'db-id', auth: { token: 'expired-token', refreshToken: 'refresh-token' },
    });
    expect(await client.pull('users', { checkpoint: 'previous' })).toEqual({
      documents: [], checkpoint: 'next', caughtUp: true,
    });
    expect(bodies).toEqual([
      { collection: 'users', checkpoint: 'previous', limit: 100 },
      { collection: 'users', checkpoint: 'previous', limit: 100 },
    ]);
  });

  it('cancels a pending request using AbortSignal', async () => {
    const admitted = deferred<void>();
    const release = deferred<void>();
    const client = serve(async () => {
      admitted.resolve();
      await release.promise;
      return Response.json(page());
    });
    const controller = new AbortController();
    const pending = client.pull('users', { signal: controller.signal }).catch(error => error);
    await admitted.promise;
    controller.abort();
    expect((await pending).code).toBe('ERR_CANCELED');
    release.resolve();
  });

  it('validates request boundaries before network access', async () => {
    let calls = 0;
    const client = serve(() => { calls++; return Response.json(page()); });
    for (const limit of [0, -1, 1.5, 1001, NaN, Infinity]) {
      await expect(client.pull('users', { limit })).rejects.toBeInstanceOf(RangeError);
    }
    for (const checkpoint of ['', 'x'.repeat(256 * 1024 + 1), '界'.repeat(90 * 1024), 123]) {
      await expect(client.pull('users', { checkpoint: checkpoint as string })).rejects.toBeInstanceOf(TypeError);
    }
    await expect(client.pull('')).rejects.toBeInstanceOf(TypeError);
    expect(calls).toBe(0);
  });
});

describe('Pull page decoding', () => {
  it('accepts known tombstone metadata and the checkpoint byte boundary', () => {
    const tombstone = { id: 'alice', collection: 'users', deleted: true, version: 2n, createdAt: 1n, updatedAt: 2n };
    expect(decodePullPage(page([tombstone], 'x'.repeat(256 * 1024), true), 'users', 1).documents).toEqual([tombstone]);
  });

  it('rejects malformed envelopes and documents before returning progress', () => {
    const badDocuments = [
      null, [], { ...live, id: '' }, { ...live, id: 'users/alice' }, { ...live, id: 'a\u0000b' },
      { ...live, collection: 'other' }, { ...live, deleted: 'true' },
      { ...live, version: 1 }, { ...live, createdAt: null }, { ...live, updatedAt: '2' },
      { id: 'alice', collection: 'users' },
      { id: 'alice', collection: 'users', deleted: true, version: 1 },
    ];
    const invalid = [
      null, [], {}, { ...page(), checkpoint: null }, { ...page(), checkpoint: '' },
      { ...page(), checkpoint: 123 }, { ...page(), checkpoint: 'x'.repeat(256 * 1024 + 1) },
      { ...page(), caughtUp: 'false' }, { ...page(), documents: {} },
      page([live, live]), { ...page(), documents: [live] }, ...badDocuments.map(document => page([document])),
    ];
    for (const response of invalid) {
      expect(() => decodePullPage(response, 'users', 1)).toThrow(TypeError);
    }
  });
});
