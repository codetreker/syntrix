import { describe, expect, it } from 'bun:test';
import { encodeQueryValue } from '../../api/value.js';
import { freezeSourceDefinition } from './records.js';
import {
  copySourceDefinition, decodeSourcePage, decodeSourceResponse, encodeSourceDefinition,
  encodeSourceRequest, sourcePageBytes,
} from './source-codec.js';
import type { SourceReadContext } from './source-types.js';

const context: SourceReadContext = {
  checkpoint: 'previous', requestId: 'read-7', signal: new AbortController().signal, sessionVersion: 2,
  expectedDatabaseIdentity: '0123456789abcdef', expectedSourceHash: 'b'.repeat(64),
};
const definition = freezeSourceDefinition({ collection: 'users', filters: [
  { field: 'count', op: '>=', value: 9007199254740993n },
] });
const doc = { id: 'alice', collection: 'users', version: 7n, createdAt: 1n, updatedAt: 2n,
  nested: { count: 9007199254740993n }, type: 'ordinary business field', deleted: false as const };
const envelope = {
  protocolVersion: 1, databaseIdentity: context.expectedDatabaseIdentity, sourceHash: context.expectedSourceHash,
  generationId: '01234567-89ab-cdef-0123-456789abcdef',
};
const eventPage = () => ({ ...envelope, mode: 'events', events: [
  { type: 'upsert', document: encodeQueryValue(doc) }, { type: 'delete', id: 'alice' },
  { type: 'upsert', document: encodeQueryValue({ ...doc, version: 1n }) }, { type: 'leave', id: 'bob' },
], checkpoint: 'new position', phase: 'live', caughtUp: true, bootstrapComplete: true });
const failure = (run: () => unknown): { code: string; message: string } => {
  try { run(); } catch (error) {
    return { code: (error as { code: string }).code, message: (error as Error).message };
  }
  throw new Error('Expected decoding to fail');
};

describe('shared replica source codec', () => {
  it('encodes the exact same typed matching-set request for HTTP and nested WS read', () => {
    const request = encodeSourceRequest(definition, context);
    expect(JSON.parse(JSON.stringify(request))).toEqual({
      collection: 'users', source: { version: 1,
        filters: [{ field: 'count', op: '>=', value: { type: 'int64', value: '9007199254740993' } }],
        orderBy: [{ field: 'id', direction: 'asc' }] },
      checkpoint: 'previous', limit: 100,
    });
    expect(Object.keys(request)).not.toContain('requestId');
    expect(Object.keys(request)).not.toContain('expectedDatabaseIdentity');
    expect(Object.keys(request)).not.toContain('expectedSourceHash');
    expect(encodeSourceRequest(definition, { ...context, checkpoint: null })).toMatchObject({ checkpoint: null });
    expect(encodeSourceDefinition(definition)).toEqual(request.source);
  });

  it('encodes complete windows without matching-set fields and retains explicit ID ordering', () => {
    const window = freezeSourceDefinition({ collection: 'users', filters: [], limit: 2,
      orderBy: [{ field: 'id', direction: 'desc' }, { field: 'score', direction: 'asc' }] });
    expect(encodeSourceRequest(window, context)).toEqual({ collection: 'users', requestId: 'read-7', source: {
      version: 1, filters: [], limit: 2,
      orderBy: [{ field: 'id', direction: 'desc' }, { field: 'score', direction: 'asc' }],
    } });
    const copy = copySourceDefinition(window);
    expect(copy).not.toBe(window);
    expect(Object.isFrozen(copy.orderBy[0])).toBe(true);
    expect(copy).toEqual(window);
  });

  it('decodes HTTP text and nested WS pages identically without collapsing repeated IDs', () => {
    const page = eventPage();
    const text = JSON.stringify(page);
    const parsedFrame = JSON.parse(JSON.stringify({ id: context.requestId, type: 'replica_page', payload: {
      subId: 'subscription-3', requestId: context.requestId, page,
    } }));
    const ws = decodeSourcePage(parsedFrame.payload.page, definition, context);
    expect(ws).toEqual(decodeSourceResponse(text, definition, context));
    if (ws.mode !== 'events') throw new Error('Expected events');
    expect(ws.events).toEqual([{ type: 'upsert', document: doc }, { type: 'delete', id: 'alice' },
      { type: 'upsert', document: { ...doc, version: 1n } }, { type: 'leave', id: 'bob' }]);
  });

  it('preserves window equivalence including the empty complete replacement', () => {
    const window = freezeSourceDefinition({ collection: 'users', filters: [], limit: 2 });
    for (const documents of [[], [encodeQueryValue(doc)]]) {
      const page = { ...envelope, mode: 'replace', requestId: context.requestId, complete: true,
        effectiveOrder: [{ field: 'id', direction: 'asc' }], documents };
      expect(decodeSourcePage(page, window, context)).toEqual(decodeSourceResponse(JSON.stringify(page), window, context));
    }
  });

  it('uses identical errors for identity, scope, metadata, and phase corruption on both transports', () => {
    for (const page of [
      { ...eventPage(), databaseIdentity: 'f'.repeat(16) },
      { ...eventPage(), sourceHash: 'c'.repeat(64) },
      { ...eventPage(), phase: 'scan' },
      { ...eventPage(), documents: [] },
      { ...eventPage(), events: [{ type: 'upsert', document: encodeQueryValue({ ...doc, collection: 'other' }) }] },
      { ...eventPage(), events: [{ type: 'upsert', document: encodeQueryValue({ ...doc, version: 7 }) }] },
      { ...eventPage(), events: [{ type: 'delete', id: 'alice', document: encodeQueryValue(doc) }] },
    ]) {
      expect(failure(() => decodeSourcePage(page, definition, context)))
        .toEqual(failure(() => decodeSourceResponse(JSON.stringify(page), definition, context)));
    }
  });

  it('enforces the page budget independently of the larger WS frame allowance before typed decoding', () => {
    const base = { ...eventPage(), events: [{ type: 'upsert', document: encodeQueryValue({ ...doc, payload: '' }) }] };
    const overhead = new TextEncoder().encode(JSON.stringify(base)).byteLength;
    const boundary = { ...base, events: [{ type: 'upsert', document: encodeQueryValue({ ...doc,
      payload: 'x'.repeat(sourcePageBytes - overhead) }) }] };
    const encoded = JSON.stringify(boundary);
    expect(encoded.length).toBe(sourcePageBytes);
    expect(decodeSourcePage(boundary, definition, context)).toEqual(decodeSourceResponse(encoded, definition, context));
    const tooLarge = { ...base, events: [{ type: 'upsert', document: encodeQueryValue({ ...doc,
      payload: 'x'.repeat(sourcePageBytes - overhead + 1) }) }] };
    const frame = JSON.stringify({ id: context.requestId, type: 'replica_page', payload: {
      subId: 'sub', requestId: context.requestId, page: tooLarge,
    } });
    expect(frame.length).toBeLessThan(sourcePageBytes + 1024);
    expect(failure(() => decodeSourcePage(tooLarge, definition, context)).code).toBe('ReplicaSourceResponseTooLarge');
    expect(failure(() => decodeSourcePage({ malformed: '界'.repeat(6 * 1024 * 1024) }, definition, context)).code)
      .toBe('ReplicaSourceResponseTooLarge');
  });
});
