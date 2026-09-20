import { afterEach, describe, expect, it } from 'bun:test';
import axios from 'axios';
import { AuthSessionChangedError, SyntrixError } from '../../api/errors.js';
import { encodeQueryValue } from '../../api/value.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { setupAuthInterceptor } from '../auth/interceptor.js';
import { createReplicaHttpUpstream, pushRequestBytes, pushProtobufBytes, pushResponseBytes, upstreamFailureDisposition } from './upstream-transport.js';
import type { PushOperation, UpstreamRequestContext } from './upstream-types.js';

const identity = '0123456789abcdef';
const context = (changes: Partial<UpstreamRequestContext> = {}): UpstreamRequestContext => ({
  signal: new AbortController().signal, sessionVersion: 0, expectedDatabaseIdentity: identity, ...changes,
});
const live = { id: 'alice', collection: 'users', version: 9007199254740993n, createdAt: 1n, updatedAt: 2n, name: 'Alice' };
const create = (logicalId = 'alice'): PushOperation => ({ logicalId, action: 'create', payload: { name: 'Alice' } });
const update = (baseVersion = 1n): PushOperation => ({ logicalId: 'alice', action: 'update', payload: { name: 'Alice' }, baseVersion });
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};
const conflict = (reason: string, current: unknown = live, changeIndex = 0, id = 'alice') => ({
  changeIndex, id, reason, current: current === null ? null : encodeQueryValue(current),
});

describe('private upstream transport', () => {
  let server: ReturnType<typeof Bun.serve> | undefined;
  afterEach(() => { server?.stop(true); server = undefined; });
  const fixture = (fetch: (request: Request) => Response | Promise<Response> = () => Response.json({ conflicts: [] }),
    database = 'db slug', collection = 'users') => {
    server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch });
    const provider = new DefaultTokenProvider({ token: 'access' });
    const http = axios.create({ baseURL: `${server.url.origin}/prefix` });
    setupAuthInterceptor(http, provider);
    return { provider, transport: createReplicaHttpUpstream({ axios: http, provider, database, collection }), http };
  };

  it('sends exact typed payloads with fixed identity, preserving prefix, namespace and zero CAS', async () => {
    let calls = 0;
    const { transport } = fixture(async request => {
      expect(new URL(request.url).pathname).toBe('/prefix/replication/v1/databases/db%20slug/push');
      expect(request.headers.get('authorization')).toBe('Bearer access');
      expect(request.headers.get('x-syntrix-expected-database-identity')).toBe(identity);
      expect(await request.json()).toEqual({ collection: 'users', changes: [
        { action: 'create', document: encodeQueryValue({ count: 9007199254740993n, fraction: 1, type: 'object', value: { _deleted: true }, id: 'alice' }) },
        { action: 'update', document: encodeQueryValue({ name: 'Alice', id: 'alice', version: 0n }) },
        { action: 'delete', document: encodeQueryValue({ id: 'alice', version: (1n << 63n) - 1n }) },
      ] });
      calls++; return Response.json({ conflicts: [] });
    });
    const input: PushOperation[] = [{ ...create(), payload: { count: 9007199254740993n, fraction: 1, type: 'object', value: { _deleted: true } } },
      update(0n), { logicalId: 'alice', action: 'delete', payload: {}, baseVersion: (1n << 63n) - 1n }];
    const batch = transport.prepare(input)[0]!;
    input[0]!.payload.count = 3n;
    expect(Object.isFrozen(batch.changes[0]!.payload)).toBe(true);
    expect(batch.httpBytes).toBe(new TextEncoder().encode(batch.body).length);
    expect(await transport.push(batch, context())).toEqual({ conflicts: [] });
    expect(calls).toBe(1);
  });

  it('prepares immutable batches with original positions, 50-row count and encoded byte boundaries', () => {
    const { transport } = fixture();
    const rows = Array.from({ length: 102 }, () => create());
    const batches = transport.prepare(rows);
    expect(batches.map(batch => batch.changes.length)).toEqual([50, 50, 2]);
    expect(batches[1]!.indexes).toEqual(Array.from({ length: 50 }, (_, i) => i + 50));
    expect(Object.isFrozen(batches)).toBe(true);
    const text = '界'.repeat(600_000);
    const byBytes = transport.prepare(Array.from({ length: 7 }, () => ({ ...create(), payload: { text } })));
    expect(byBytes.length).toBe(2);
    for (const batch of byBytes) {
      expect(batch.httpBytes).toBeLessThanOrEqual(pushRequestBytes);
      expect(batch.protobufBytes).toBeLessThanOrEqual(pushProtobufBytes);
    }
    expect(transport.prepare([])).toEqual([]);
  });

  it('accounts for Go JSON escaping and duplicated protobuf path fields independently of HTTP bytes', () => {
    const { transport, provider, http } = fixture();
    expect(() => transport.prepare([{ ...create(), payload: { text: '<'.repeat(4 * 1024 * 1024) } }])).toThrow('budget');
    const escaped = transport.prepare(Array.from({ length: 4 }, () => ({ ...create(), payload: { text: '<'.repeat(1500 * 1024) } })));
    expect(escaped.map(batch => batch.changes.length)).toEqual([2, 2]);
    expect(escaped[0]!.httpBytes).toBeLessThan(pushRequestBytes);
    const long = createReplicaHttpUpstream({ axios: http, provider, database: 'db', collection: 'c'.repeat(300_000) });
    const repeated = long.prepare(Array.from({ length: 50 }, () => create()));
    expect(repeated.length).toBeGreaterThan(1);
    for (const batch of repeated) expect(batch.protobufBytes).toBeLessThanOrEqual(pushProtobufBytes);
  });

  it('rejects invalid/oversized items and foreign batches before dispatch or callback', async () => {
    let calls = 0, dispatches = 0;
    const { transport, http, provider } = fixture(() => { calls++; return Response.json({ conflicts: [] }); });
    for (const change of [
      { ...create(), logicalId: 'alice/bob' }, { ...create(), logicalId: '😀' }, { ...create(), logicalId: 'a'.repeat(65) },
      { ...create(), baseVersion: 1n }, { ...update(), baseVersion: undefined }, { ...update(), baseVersion: -1n },
      { ...update(), baseVersion: 1n << 63n }, { ...update(), baseVersion: 1 as unknown as bigint },
      { ...create(), payload: { version: 1n } }, { ...create(), payload: { value: undefined } },
      { logicalId: 'alice', action: 'delete', payload: { name: 'a' }, baseVersion: 1n },
      { ...create(), action: 'invalid' },
    ]) expect(() => transport.prepare([change as PushOperation])).toThrow();
    expect(() => transport.prepare([create(), { ...create(), payload: { text: 'x'.repeat(pushRequestBytes) } }])).toThrow('budget');
    const other = createReplicaHttpUpstream({ axios: http, provider, database: 'db slug', collection: 'users' });
    const error = await transport.push(other.prepare([create()])[0]!, { ...context(), beforeDispatch: async () => { dispatches++; } }).catch(error => error);
    expect(upstreamFailureDisposition(error)).toBe('not-dispatched');
    expect(dispatches).toBe(0); expect(calls).toBe(0);
  });

  it('retains original thrown callback/cancellation objects and never dispatches without binding', async () => {
    let calls = 0;
    const { transport, provider } = fixture(() => { calls++; return Response.json({ conflicts: [] }); });
    const batch = transport.prepare([create()])[0]!;
    const failure = new Error('marker write failed');
    const result = await transport.push(batch, { ...context(), beforeDispatch: async () => { throw failure; } }).catch(error => error);
    expect(result).toBe(failure); expect(upstreamFailureDisposition(result)).toBe('not-dispatched');
    await expect(transport.push(batch, context({ expectedDatabaseIdentity: '' }))).rejects.toThrow('bound');
    const controller = new AbortController(); controller.abort(failure);
    expect(await transport.push(batch, context({ signal: controller.signal })).catch(error => error)).toBe(failure);
    const changed = await transport.push(batch, { ...context(), beforeDispatch: async () => { provider.setToken('other'); } }).catch(error => error);
    expect(changed).toBeInstanceOf(AuthSessionChangedError); expect(upstreamFailureDisposition(changed)).toBe('not-dispatched');
    expect(calls).toBe(0);
  });

  it('decodes complete conflicts by request index, including repeated IDs and later missing create targets', async () => {
    const tombstone = { id: 'alice', collection: 'users', version: 5n, createdAt: 1n, updatedAt: 3n, deleted: true };
    const conflicts = [conflict('already_exists', live, 0), conflict('version_mismatch', live, 1),
      conflict('tombstoned', tombstone, 2), conflict('missing', null, 3), conflict('precondition_failed', { ...live, version: 1n }, 4)];
    const { transport } = fixture(() => Response.json({ conflicts }));
    const batch = transport.prepare([create(), update(), update(), create(), update(), create('bob')])[0]!;
    const response = await transport.push(batch, context());
    expect(response.conflicts.map(c => c.changeIndex)).toEqual([0, 1, 2, 3, 4]);
    expect(response.conflicts[1]!.current).toEqual(live);
    expect(response.conflicts[2]!.current).toEqual(tombstone);
    expect(response.conflicts[3]!.current).toBeNull();
  });

  it('rejects the whole successful HTTP result on malformed conflicts and classifies it unknown', async () => {
    let response: unknown;
    const { transport } = fixture(() => Response.json(response));
    const batch = transport.prepare([update(), create()])[0]!;
    for (const conflicts of [null, {}, [conflict('missing', null, -1)], [conflict('missing', null, 2)],
      [conflict('missing', null, 1), conflict('missing', null, 0)], [conflict('missing', null), conflict('missing', null)],
      [conflict('missing', null, 0, 'bob')], [conflict('missing', live)], [conflict('version_mismatch', { ...live, version: 1n })],
      [conflict('already_exists', live)], [conflict('unknown', live)], [conflict('precondition_failed', live)],
      [conflict('tombstoned', { ...live, deleted: true })], [conflict('version_mismatch', { ...live, collection: 'other' })],
      [conflict('version_mismatch', { ...live, version: 1 })], [conflict('version_mismatch', { ...live, version: -1n })],
      [{ ...conflict('missing', null), extra: 1 }], [{ changeIndex: 0, id: 'alice', reason: 'missing' }],
      [{ ...conflict('missing', null), current: { type: 'null' } }]]) {
      response = { conflicts };
      const error = await transport.push(batch, context()).catch(error => error);
      expect(error).toBeInstanceOf(Error); expect(upstreamFailureDisposition(error)).toBe('unknown');
    }
    for (const value of [null, [], { conflicts: [], extra: 1 }]) {
      response = value;
      await expect(transport.push(batch, context())).rejects.toThrow();
    }
  });

  it('distinguishes known pre-operation rejection from possible committed-prefix errors', async () => {
    let status = 409, code = 'DATABASE_IDENTITY_MISMATCH';
    const { transport } = fixture(() => Response.json({ code, message: code }, { status }));
    const batch = transport.prepare([create()])[0]!;
    for (const [nextStatus, nextCode, expected] of [
      [409, 'DATABASE_IDENTITY_MISMATCH', 'not-executed'], [429, 'RATE_LIMITED', 'not-executed'],
      [422, 'REPLICATION_BUDGET_EXCEEDED', 'unknown'], [500, 'INTERNAL_ERROR', 'unknown'], [400, 'BAD_REQUEST', 'unknown'],
    ] as const) {
      status = nextStatus; code = nextCode;
      const error = await transport.push(batch, context()).catch(error => error);
      expect(error).toBeInstanceOf(SyntrixError); expect(error.code).toBe(code);
      expect(upstreamFailureDisposition(error)).toBe(expected);
    }
    expect(upstreamFailureDisposition(new Error('unclassified'))).toBe('unknown');
    expect(upstreamFailureDisposition('primitive')).toBe('unknown');
  });

  it('treats lost, malformed and oversized successful bodies as unknown without retry', async () => {
    let response = 'not JSON', calls = 0;
    const { transport } = fixture(() => { calls++; return new Response(response); });
    const batch = transport.prepare([create()])[0]!;
    for (const value of ['not JSON', 'x'.repeat(pushResponseBytes + 1)]) {
      response = value;
      const error = await transport.push(batch, context()).catch(error => error);
      expect(error).toBeInstanceOf(Error); expect(upstreamFailureDisposition(error)).toBe('unknown');
    }
    expect(calls).toBe(2);
  });

  it('queries the authoritative single ID with no source predicates or ordering and retains tombstones', async () => {
    let documents: unknown[] = [];
    const { transport } = fixture(async request => {
      expect(new URL(request.url).pathname).toBe('/prefix/api/v1/databases/db%20slug/query');
      expect(request.headers.get('x-syntrix-expected-database-identity')).toBe(identity);
      expect(await request.json()).toEqual({ collection: 'users', filters: [{ field: 'id', op: '==', value: { type: 'string', value: 'alice' } }], limit: 1, showDeleted: true });
      return Response.json({ documents, nextCursor: null, effectiveOrder: [{ field: 'id', direction: 'asc' }] });
    });
    expect(await transport.readCurrent('alice', context())).toBeNull();
    documents = [encodeQueryValue(live)];
    expect(await transport.readCurrent('alice', context())).toEqual(live);
    const deleted = { id: 'alice', collection: 'users', version: 9n, createdAt: 1n, updatedAt: 5n, deleted: true };
    documents = [encodeQueryValue(deleted)];
    expect(await transport.readCurrent('alice', context())).toEqual(deleted);
  });

  it('rejects incomplete, unrelated or malformed ID reads and propagates identity failures', async () => {
    const base = { documents: [], nextCursor: null, effectiveOrder: [{ field: 'id', direction: 'asc' }] };
    let response: unknown = base, status = 200;
    const { transport } = fixture(() => Response.json(response, { status }));
    for (const value of [{ ...base, nextCursor: 'more' }, { ...base, effectiveOrder: [] },
      { ...base, effectiveOrder: [{ field: 'id', direction: 'desc' }] }, { ...base, extra: true },
      { ...base, documents: [encodeQueryValue(live), encodeQueryValue(live)] },
      { ...base, documents: [encodeQueryValue({ ...live, id: 'bob' })] },
      { ...base, documents: [{ type: 'object', value: { version: { type: 'int64', value: '01' } } }] }]) {
      response = value;
      await expect(transport.readCurrent('alice', context())).rejects.toThrow();
    }
    status = 409; response = { code: 'DATABASE_IDENTITY_MISMATCH', message: 'reassigned' };
    const error = await transport.readCurrent('alice', context()).catch(error => error);
    expect(error).toBeInstanceOf(SyntrixError); expect(error.code).toBe('DATABASE_IDENTITY_MISMATCH');
    status = 404; response = { code: 'DATABASE_NOT_FOUND', message: 'missing database' };
    await expect(transport.readCurrent('alice', context())).rejects.toBeInstanceOf(SyntrixError);
  });

  it('rejects late old-session success and propagates cancellation after possible dispatch as unknown', async () => {
    const admitted = deferred(), release = deferred();
    const { transport, provider } = fixture(async () => { admitted.resolve(); await release.promise; return Response.json({ conflicts: [] }); });
    const batch = transport.prepare([create()])[0]!;
    const operation = transport.push(batch, context()).catch(error => error);
    await admitted.promise; provider.setToken('other'); release.resolve();
    const error = await operation;
    expect(error).toBeInstanceOf(AuthSessionChangedError); expect(upstreamFailureDisposition(error)).toBe('unknown');
  });

  it('keeps original identity through a same-session authentication refresh', async () => {
    const bodies: unknown[] = [];
    server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: async request => {
      if (new URL(request.url).pathname.endsWith('/auth/v1/refresh')) {
        return Response.json({ access_token: 'fresh-token', refresh_token: 'next-refresh' });
      }
      expect(request.headers.get('x-syntrix-expected-database-identity')).toBe(identity);
      bodies.push(await request.json());
      if (request.headers.get('authorization') === 'Bearer expired-token') return Response.json({ code: 'UNAUTHORIZED', message: 'Expired' }, { status: 401 });
      expect(request.headers.get('authorization')).toBe('Bearer fresh-token');
      return Response.json({ conflicts: [] });
    } });
    const provider = new DefaultTokenProvider({ token: 'expired-token', refreshToken: 'refresh-token' }, `${server.url.origin}/prefix`);
    const http = axios.create({ baseURL: `${server.url.origin}/prefix` });
    setupAuthInterceptor(http, provider);
    const transport = createReplicaHttpUpstream({ axios: http, provider, database: 'db', collection: 'users' });
    const requestContext = { ...context(), beforeDispatch: async () => { requestContext.expectedDatabaseIdentity = 'f'.repeat(16); } };
    await transport.push(transport.prepare([create()])[0]!, requestContext);
    expect(bodies).toHaveLength(2); expect(bodies[0]).toEqual(bodies[1]);
  });

  it('does not downgrade uncertainty when a sibling reuses an owner cancellation error', async () => {
    const admitted = deferred(), release = deferred();
    const { transport } = fixture(async () => { admitted.resolve(); await release.promise; return Response.json({ conflicts: [] }); });
    const batch = transport.prepare([create()])[0]!;
    const controller = new AbortController();
    const failure = new Error('owner retired');
    const pending = transport.push(batch, context({ signal: controller.signal })).catch(error => error);
    await admitted.promise; controller.abort(failure);
    const canceled = await pending;
    expect(canceled.code).toBe('ERR_CANCELED'); expect(canceled.cause).toBe(failure);
    expect(upstreamFailureDisposition(canceled)).toBe('unknown');
    release.resolve();
    const reused = await transport.push(batch, { ...context(), beforeDispatch: async () => { throw canceled; } }).catch(error => error);
    expect(reused).toBe(canceled); expect(upstreamFailureDisposition(reused)).toBe('unknown');
  });

  it('admits conflict JSON larger than the Pull limit and rejects malformed error bodies without masking', async () => {
    const large = { ...live, content: 'x'.repeat(17 * 1024 * 1024) };
    let malformed = false;
    const { transport } = fixture(() => malformed ? new Response('upstream unavailable', { status: 503 }) : Response.json({ conflicts: [conflict('already_exists', large)] }));
    const batch = transport.prepare([create()])[0]!;
    const result = await transport.push(batch, context());
    expect(result.conflicts[0]!.current!.content).toBe(large.content);
    malformed = true;
    const error = await transport.push(batch, context()).catch(error => error);
    expect(axios.isAxiosError(error)).toBe(true); expect(error.response.data).toBe('upstream unavailable');
    expect(upstreamFailureDisposition(error)).toBe('unknown');
  });
});
