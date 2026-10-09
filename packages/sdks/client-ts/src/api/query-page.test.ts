import { describe, expect, it, mock } from 'bun:test';
import { CollectionReferenceImpl } from './reference';
import { encodeQueryValue } from './value';
import { QueryOrder } from './types';
import { RestTransport } from '../internal/transport/rest-transport';

describe('Query pages', () => {
  it('decodes full documents, passes opaque cursors, and preserves effective order', async () => {
    const document = { id: 'a', version: 9007199254740993n, createdAt: 42n, score: 1.5, nested: { value: 9223372036854775807n } };
    const order: QueryOrder[] = [{ field: 'score', direction: 'desc' }, { field: 'id', direction: 'asc' }];
    const post = mock(async (_path: string, _query: unknown) => ({ data: { documents: [encodeQueryValue(document)], nextCursor: 'opaque-token' as string | null, effectiveOrder: order } }));
    const collection = new CollectionReferenceImpl<typeof document>(new RestTransport({ post } as any, 'db'), 'items');
    const page = await collection.where('version', 'in', [9007199254740992n, 9007199254740993n]).orderBy('score', 'desc').limit(1).getPage();
    expect(page).toEqual({ documents: [document], nextCursor: 'opaque-token', effectiveOrder: order });
    expect(post.mock.calls[0]).toEqual(['/api/v1/databases/db/query', {
      collection: 'items',
      filters: [{ field: 'version', op: 'in', value: { type: 'array', value: [{ type: 'int64', value: '9007199254740992' }, { type: 'int64', value: '9007199254740993' }] } }],
      orderBy: [{ field: 'score', direction: 'desc' }],
      limit: 1,
    }]);
    post.mockResolvedValue({ data: { documents: [], nextCursor: null, effectiveOrder: order } });
    expect(await collection.startAfter(page.nextCursor!).getPage()).toEqual({ documents: [], nextCursor: null, effectiveOrder: order });
    expect((post.mock.calls[1][1] as { startAfter: string }).startAfter).toBe('opaque-token');
  });

  it('get returns exactly one selected page', async () => {
    const post = mock(async () => ({ data: { documents: [encodeQueryValue({ id: 'a' })], nextCursor: 'more', effectiveOrder: [] } }));
    const collection = new CollectionReferenceImpl(new RestTransport({ post } as any, 'db'), 'items');
    expect(await collection.get()).toEqual([{ id: 'a' }]);
    expect(post).toHaveBeenCalledTimes(1);
  });

  it('rejects invalid filter values before sending requests', async () => {
    const post = mock(async () => ({ data: {} }));
    const collection = new CollectionReferenceImpl(new RestTransport({ post } as any, 'db'), 'items');
    await expect(collection.where('score', '==', NaN).getPage()).rejects.toThrow('finite');
    expect(post).not.toHaveBeenCalled();
  });

  it('propagates transport errors without replacement', async () => {
    const error = new Error('permission denied');
    const post = mock(async () => { throw error; });
    const collection = new CollectionReferenceImpl(new RestTransport({ post } as any, 'db'), 'items');
    await expect(collection.getPage()).rejects.toBe(error);
  });

  it.each([
    { docs: [] }, { documents: [], effectiveOrder: [] },
    { documents: [], nextCursor: null, effectiveOrder: null },
    { documents: [], nextCursor: 1, effectiveOrder: [] },
    { documents: [{ type: 'int64', value: '1' }], nextCursor: null, effectiveOrder: [] },
    { documents: [], nextCursor: null, effectiveOrder: [{ field: 'id', direction: 'up' }] },
  ])('rejects malformed page %#', async data => {
    const transport = new RestTransport({ post: async () => ({ data }) } as any, 'db');
    await expect(transport.queryPage('/api/v1/query', {})).rejects.toThrow();
  });
});
