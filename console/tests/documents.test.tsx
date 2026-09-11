import { afterEach, describe, expect, it, mock } from 'bun:test';
import { renderToStaticMarkup } from 'react-dom/server';
import { api, dataApi } from '../src/lib/api';
import { documentsApi, documentData, formatDocumentDate, formatDocumentJson, hasInt64Value, parseFilterValue } from '../src/lib/documents';
import { DocumentViewer } from '../src/components/features/data-browser/DocumentViewer';
import { DocumentEditor } from '../src/components/features/data-browser/DocumentEditor';
import { encodeQueryValue } from '../../sdk/syntrix-client-ts/src/api/value';

const originalPost = api.post;
afterEach(() => { api.post = originalPost; });

describe('Console query pages', () => {
  it('uses the database route, typed filters, requested limit, and server continuation', async () => {
    const documents = [{ id: 'doc-a', score: 9007199254740993n, version: 1n }];
    const effectiveOrder = [{ field: 'score', direction: 'asc' }];
    const post = mock(async () => ({ data: {
      documents: documents.map(encodeQueryValue), nextCursor: 'opaque:not-a-document-id', effectiveOrder,
    } }));
    api.post = post as typeof api.post;
    const page = await documentsApi.query({
      database: 'project/db', collection: 'items', limit: 2,
      filters: [{ field: 'score', op: '>=', value: parseFilterValue('9007199254740993') }],
      orderBy: [{ field: 'score', direction: 'asc' }],
    });
    expect(page).toEqual({ documents, nextCursor: 'opaque:not-a-document-id', effectiveOrder });
    expect(post).toHaveBeenCalledWith('/api/v1/databases/project%2Fdb/query', {
      collection: 'items', limit: 2,
      filters: [{ field: 'score', op: '>=', value: { type: 'int64', value: '9007199254740993' } }],
      orderBy: effectiveOrder,
    });
    post.mockResolvedValue({ data: { documents: [], nextCursor: null as any, effectiveOrder } });
    expect(await documentsApi.query({ database: 'project/db', collection: 'items', startAfter: page.nextCursor! }))
      .toEqual({ documents: [], nextCursor: null, effectiveOrder });
    expect(post).toHaveBeenLastCalledWith('/api/v1/databases/project%2Fdb/query', {
      collection: 'items', limit: 20, filters: [], orderBy: [], startAfter: 'opaque:not-a-document-id',
    });
  });

  it('rejects malformed pages and preserves transport errors', async () => {
    api.post = mock(async () => ({ data: [] })) as typeof api.post;
    await expect(dataApi.query({ database: 'db', collection: 'items' })).rejects.toThrow('page');
    const error = new Error('INDEX_UNAVAILABLE');
    api.post = mock(async () => { throw error; }) as typeof api.post;
    await expect(documentsApi.query({ database: 'db', collection: 'items' })).rejects.toBe(error);
  });

  it('parses exact integers and rejects out-of-domain numbers', () => {
    expect(parseFilterValue('9223372036854775807')).toBe(9223372036854775807n);
    expect(parseFilterValue('1.0')).toBe(1);
    expect(parseFilterValue('1e3')).toBe(1000);
    expect(parseFilterValue('null')).toBeNull();
    expect(parseFilterValue('true')).toBe(true);
    expect(parseFilterValue('false')).toBe(false);
    expect(parseFilterValue('name')).toBe('name');
    expect(() => parseFilterValue('9223372036854775808')).toThrow('int64');
    expect(() => parseFilterValue('1e999')).toThrow('finite');
  });
});

describe('Console exact document display', () => {
  it('prints integer tokens exactly without changing business strings or objects', () => {
    const value = { number: 9007199254740993n, nested: [{ type: 'int64', value: '123' }, -9223372036854775808n], empty: [], object: {}, nil: null };
    expect(formatDocumentJson(value, 0)).toBe('{"number":9007199254740993,"nested":[{"type":"int64","value":"123"},-9223372036854775808],"empty":[],"object":{},"nil":null}');
    expect(formatDocumentJson({ n: 9007199254740993n })).toBe('{\n  "n": 9007199254740993\n}');
  });

  it('strips metadata from edits and detects nested business int64 values', () => {
    const metadata = { id: 'a', collection: 'items', version: 1n, createdAt: 1n, updatedAt: 1n, deleted: false };
    expect(documentData({ ...metadata, name: 'a' })).toEqual({ name: 'a' });
    expect(hasInt64Value(documentData({ ...metadata, name: 'a' }))).toBe(false);
    expect(hasInt64Value(documentData({ ...metadata, nested: [{ n: 1n }] }))).toBe(true);
    expect(formatDocumentDate(1726070400000n)).not.toBe('-');
    expect(formatDocumentDate(9223372036854775807n)).toBe('-');
  });

  it('renders exact integers and escapes document HTML in the viewer', () => {
    const markup = renderToStaticMarkup(<DocumentViewer
      document={{ id: 'a', score: 9007199254740993n, value: '<img src=x onerror=alert(1)>' }}
      onClose={() => {}} onEdit={() => {}} onDelete={() => {}}
    />);
    expect(markup).toContain('9007199254740993');
    expect(markup).not.toContain('<img');
    expect(markup).toContain('&lt;img');
  });

  it('makes documents with business int64 fields explicitly read-only', () => {
    const markup = renderToStaticMarkup(<DocumentEditor
      document={{ id: 'a', nested: { amount: 9007199254740993n } }}
      database="db" collection="items" onClose={() => {}} onSave={() => {}}
    />);
    expect(markup).toContain('read-only here');
    expect(markup).toContain('readOnly=""');
    expect(markup).toContain('disabled=""');
  });
});
