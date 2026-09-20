import { afterEach, describe, expect, it } from 'bun:test';
import axios from 'axios';
import { AuthSessionChangedError, SyntrixError } from '../../api/errors.js';
import { encodeQueryValue } from '../../api/value.js';
import { setupAuthInterceptor } from '../auth/interceptor.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaHttpSource, decodeSourceResponse, sourcePageBytes } from './source.js';
import type { SourceReadContext } from './source-types.js';
import type { FrozenSourceDefinition } from './storage-types.js';

const definition: FrozenSourceDefinition = { collection: 'users', filters: [], orderBy: [], limit: null };
const live = { id: 'alice', collection: 'users', version: 9007199254740993n, createdAt: 1n, updatedAt: 2n, name: 'Alice' };
const identity = { protocolVersion: 1, databaseIdentity: '0123456789abcdef', sourceHash: 'a'.repeat(64), generationId: '01234567-89ab-cdef-0123-456789abcdef' };
const events = (entries: unknown[] = [{ type: 'upsert', document: encodeQueryValue(live) }]) => ({
  ...identity, mode: 'events', events: entries, checkpoint: 'opaque', phase: 'scan', caughtUp: false, bootstrapComplete: false,
});
const windowPage = (documents: unknown[] = [encodeQueryValue(live)]) => ({
  ...identity, mode: 'replace', requestId: 'request-1', complete: true,
  effectiveOrder: [{ field: 'id', direction: 'asc' }], documents,
});
const context = (changes: Partial<SourceReadContext> = {}): SourceReadContext => ({
  checkpoint: null, requestId: 'request-1', signal: new AbortController().signal, sessionVersion: 0,
  expectedDatabaseIdentity: null, expectedSourceHash: null, ...changes,
});
const decode = (page: unknown, def = definition, ctx = context()) => decodeSourceResponse(JSON.stringify(page), def, ctx);
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};

describe('query-source wire decoder', () => {
  it('preserves repeated IDs, exact int64 metadata, minimal deletes, and source order', () => {
    const input = [{ type: 'upsert', document: encodeQueryValue(live) }, { type: 'delete', id: 'alice' },
      { type: 'upsert', document: encodeQueryValue({ ...live, version: 1n }) }, { type: 'leave', id: '陌生😀' }];
    const page = decode(events(input));
    expect(page.mode).toBe('events');
    if (page.mode !== 'events') throw new Error('mode');
    expect(page.events).toEqual([{ type: 'upsert', document: live }, { type: 'delete', id: 'alice' },
      { type: 'upsert', document: { ...live, version: 1n } }, { type: 'leave', id: '陌生😀' }]);
    expect(decode(events([]))).toMatchObject({ checkpoint: 'opaque', events: [], caughtUp: false });
    expect(decode({ ...events([]), phase: 'live', caughtUp: false, bootstrapComplete: true })).toMatchObject({ phase: 'live' });
    expect(decode({ ...events([]), phase: 'live', caughtUp: true, bootstrapComplete: true })).toMatchObject({ caughtUp: true });
    expect(decode({ ...events([]), phase: 'replay' })).toMatchObject({ phase: 'replay' });
  });

  it('rejects malformed envelope identities, unsupported fields, counts, and progress combinations', () => {
    const bad = [null, [], { ...events(), mode: 'replace' }, { ...events(), extra: 1 },
      { ...events(), protocolVersion: 2 }, { ...events(), databaseIdentity: 'ABCDEF0123456789' },
      { ...events(), sourceHash: 'a'.repeat(63) }, { ...events(), generationId: '00000000-0000-0000-0000-000000000000' },
      { ...events(), generationId: identity.generationId.toUpperCase() }, { ...events(), checkpoint: '' },
      { ...events(), checkpoint: '界'.repeat(90 * 1024) }, { ...events(), phase: 'unknown' },
      { ...events(), caughtUp: true }, { ...events(), bootstrapComplete: true },
      { ...events(), caughtUp: 0 }, { ...events(), phase: 'live', bootstrapComplete: false },
      events(Array.from({ length: 101 }, () => ({ type: 'leave', id: 'a' })))];
    for (const page of bad) expect(() => decode(page)).toThrow();
    const missing = events() as Record<string, unknown>;
    delete missing.caughtUp;
    expect(() => decode(missing)).toThrow();
  });

  it('rejects invalid event shapes, document scope, metadata types, and tombstone upserts', () => {
    for (const item of [null, { type: 'unknown', id: 'a' }, { type: 'leave', id: '' },
      { type: 'delete', id: 'a/b' }, { type: 'delete', id: 'a\0' }, { type: 'delete', id: '\ud800' },
      { type: 'delete', id: '\udc00' }, { type: 'delete', id: 'a', observedMetadata: {} },
      { type: 'upsert', document: { type: 'int64', value: '4' } },
      { type: 'upsert', document: { type: 'object', value: { id: { type: 'invalid' } } } },
      ...[{ ...live, collection: 'elsewhere' }, { ...live, deleted: true }, { ...live, version: 2 }, { ...live, version: -1n },
        { ...live, createdAt: undefined }].map(value => ({ type: 'upsert', document: encodeQueryValue(
          Object.fromEntries(Object.entries(value).filter(([, entry]) => entry !== undefined))) }))]) {
      expect(() => decode(events([item]))).toThrow();
    }
  });

  it('validates complete windows, unique members, exact order, and request correlation', () => {
    const def = { ...definition, limit: 2, orderBy: [{ field: 'score', direction: 'desc' as const }] };
    const page = { ...windowPage(), effectiveOrder: [...def.orderBy, { field: 'id', direction: 'asc' }] };
    expect(decode(page, def)).toMatchObject({ documents: [live], complete: true });
    expect(decode(windowPage([]), { ...definition, limit: 1 })).toMatchObject({ documents: [] });
    expect(decode({ ...windowPage(), effectiveOrder: [{ field: 'id', direction: 'desc' }] },
      { ...definition, limit: 1, orderBy: [{ field: 'id', direction: 'desc' }] })).toMatchObject({ documents: [live] });
    for (const bad of [{ ...page, complete: false }, { ...page, requestId: 'stale' }, { ...page, checkpoint: null },
      { ...page, effectiveOrder: [] }, { ...page, effectiveOrder: [def.orderBy[0], { field: 'id', direction: 'desc' }] },
      { ...page, effectiveOrder: [def.orderBy[0], { field: 'id', direction: 'asc', extra: 1 }] },
      { ...page, documents: [encodeQueryValue(live), encodeQueryValue(live)] },
      { ...page, documents: Array.from({ length: 3 }, () => encodeQueryValue(live)) }]) {
      expect(() => decode(bad, def)).toThrow();
    }
  });

  it('rejects binding changes before decoding documents and bounds raw JSON before parsing', () => {
    expect(() => decode(events([{ bad: true }]), definition, context({ expectedDatabaseIdentity: 'f'.repeat(16) })))
      .toThrow('database identity');
    expect(() => decode(events([{ bad: true }]), definition, context({ expectedSourceHash: 'b'.repeat(64) })))
      .toThrow('Source hash');
    expect(() => decodeSourceResponse('x'.repeat(sourcePageBytes + 1), definition, context())).toThrow('16 MiB');
    expect(() => decodeSourceResponse('界'.repeat(6 * 1024 * 1024), definition, context())).toThrow('16 MiB');
    expect(() => decodeSourceResponse('{', definition, context())).toThrow('valid JSON');
    expect(() => decodeSourceResponse({} as string, definition, context())).toThrow('JSON text');
  });
});

describe('query-source HTTP transport', () => {
  let server: ReturnType<typeof Bun.serve> | undefined;
  afterEach(() => { server?.stop(true); server = undefined; });
  const serve = (fetch: (request: Request) => Response | Promise<Response>, def = definition) => {
    server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch });
    const provider = new DefaultTokenProvider({ token: 'access-token' });
    const http = axios.create({ baseURL: `${server.url.origin}/prefix` });
    setupAuthInterceptor(http, provider);
    return { provider, source: createReplicaHttpSource({ axios: http, provider, database: 'db slug', definition: def }) };
  };
  it('retains prefix, typed query and the bound database header on resume and null-cursor rebuild', async () => {
    const requests: unknown[] = [];
    const { source } = serve(async request => {
      expect(new URL(request.url).pathname).toBe('/prefix/replication/v1/databases/db%20slug/pull');
      expect(request.headers.get('authorization')).toBe('Bearer access-token');
      expect(request.headers.get('x-syntrix-expected-database-identity')).toBe(identity.databaseIdentity);
      requests.push(await request.json());
      return Response.json(events());
    }, { ...definition, filters: [{ field: 'score', op: '>', value: { type: 'int64', value: '9007199254740993' } }] });
    for (const cp of ['previous', null]) {
      const page = await source.read(context({ checkpoint: cp, expectedDatabaseIdentity: identity.databaseIdentity, expectedSourceHash: identity.sourceHash }));
      expect(page.mode).toBe('events');
    }
    expect(requests).toEqual(['previous', null].map(checkpoint => ({ collection: 'users', checkpoint, limit: 100,
      source: { version: 1, filters: source.definition.filters, orderBy: [] } })));
  });
  it('omits checkpoint and transfer limit for windows and freezes the configured query', async () => {
    const def = { ...definition, limit: 2 };
    const { source } = serve(async request => {
      expect(await request.json()).toEqual({ collection: 'users', requestId: 'request-1',
        source: { version: 1, filters: [], orderBy: [], limit: 2 } });
      expect(request.headers.has('x-syntrix-expected-database-identity')).toBe(false);
      return Response.json(windowPage());
    }, def);
    def.limit = 9;
    expect(Object.isFrozen(source.definition.filters)).toBe(true);
    expect((await source.read(context({ checkpoint: 'ignored-window-cursor' }))).mode).toBe('replace');
  });
  it('accepts legal responses over 4 MiB and rejects oversized transport bodies', async () => {
    let large = false;
    const { source } = serve(() => Response.json(events([{ type: 'upsert', document: encodeQueryValue({ ...live,
      text: 'x'.repeat((large ? 17 : 5) * 1024 * 1024) }) }])));
    const page = await source.read(context());
    if (page.mode !== 'events' || page.events[0]?.type !== 'upsert') throw new Error('mode');
    expect((page.events[0].document.text as string).length).toBe(5 * 1024 * 1024);
    large = true;
    await expect(source.read(context())).rejects.toThrow();
  });
  it('preserves retry/resync/identity errors through the authentication interceptor', async () => {
    let code = 'RESYNC_REQUIRED';
    const { source } = serve(() => Response.json({ code, message: code }, { status: 409 }));
    for (const next of ['RESYNC_REQUIRED', 'DATABASE_IDENTITY_MISMATCH', 'REPLICATION_UNAVAILABLE']) {
      code = next;
      const error = await source.read(context()).catch(error => error);
      expect(error).toBeInstanceOf(SyntrixError);
      expect(error.code).toBe(code);
      expect(error.status).toBe(409);
    }
  });
  it('preserves transport failures and malformed server error bodies', async () => {
    const { source } = serve(() => new Response('not JSON', { status: 503 }));
    const error = await source.read(context()).catch(error => error);
    expect(axios.isAxiosError(error)).toBe(true);
    expect(error.response.data).toBe('not JSON');
  });
  it('rejects stale admission before dispatch and rejects successful late old-session responses', async () => {
    const admitted = deferred();
    const release = deferred();
    let calls = 0;
    const { source, provider } = serve(async () => { calls++; admitted.resolve(); await release.promise; return Response.json(events()); });
    const result = source.read(context()).catch(error => error);
    await admitted.promise;
    provider.setToken('different');
    release.resolve();
    expect(await result).toBeInstanceOf(AuthSessionChangedError);
    await expect(source.read(context())).rejects.toBeInstanceOf(AuthSessionChangedError);
    expect(calls).toBe(1);
  });
  it('cancels pending HTTP and honors an already canceled owner', async () => {
    const admitted = deferred();
    const release = deferred();
    const { source } = serve(async () => { admitted.resolve(); await release.promise; return Response.json(events()); });
    const controller = new AbortController();
    const reason = new Error('owner closed');
    const result = source.read(context({ signal: controller.signal })).catch(error => error);
    await admitted.promise;
    controller.abort(reason);
    const error = await result;
    expect(error.code).toBe('ERR_CANCELED');
    expect(error.cause).toBe(reason);
    release.resolve();
    expect(await source.read(context({ signal: controller.signal })).catch(error => error)).toBe(reason);
  });
  it('validates local request bounds before dispatch', async () => {
    let calls = 0;
    const { source, provider } = serve(() => { calls++; return Response.json(events()); });
    for (const changes of [{ requestId: '' }, { checkpoint: '' }, { expectedDatabaseIdentity: 'bad' }, { expectedSourceHash: 'bad' }]) {
      await expect(source.read(context(changes))).rejects.toThrow();
    }
    for (const limit of [0, -1, 1.1, 1001]) expect(() => createReplicaHttpSource({ axios: axios.create(), provider,
      database: 'db', definition: { ...definition, limit } })).toThrow();
    expect(calls).toBe(0);
  });
});
