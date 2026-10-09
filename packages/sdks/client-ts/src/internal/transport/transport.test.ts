import { describe, it, expect, mock } from 'bun:test';
import { RestTransport } from './rest-transport';

const TEST_DB = 'test-db';

describe('RestTransport', () => {
  it('should return null on 404', async () => {
    const mockAxios = {
      get: mock(async () => {
        const err: any = new Error('Not Found');
        err.response = { status: 404 };
        throw err;
      }),
    } as any;

    const transport = new RestTransport(mockAxios, TEST_DB);
    const result = await transport.get('/foo');
    expect(result).toBeNull();
  });

  it('should throw on other errors', async () => {
    const mockAxios = {
      get: mock(async () => {
        const err: any = new Error('Server Error');
        err.response = { status: 500 };
        throw err;
      }),
    } as any;

    const transport = new RestTransport(mockAxios, TEST_DB);
    try {
        await transport.get('/foo');
        expect(true).toBe(false);
    } catch (e: any) {
        expect(e.response.status).toBe(500);
    }
  });

  it('should create resource', async () => {
    const mockAxios = {
      post: mock(async () => ({ data: { id: '123' } })),
    } as any;
    const transport = new RestTransport(mockAxios, TEST_DB);
    const result = await transport.create<any>('/foo', { bar: 'baz' });
    expect(result).toEqual({ id: '123' });
    expect(mockAxios.post).toHaveBeenCalledWith('/api/v1/databases/test-db/documents/foo', { bar: 'baz' });
  });

  it('should set resource', async () => {
    const mockAxios = {
      put: mock(async () => ({ data: { id: '123' } })),
    } as any;
    const transport = new RestTransport(mockAxios, TEST_DB);
    const result = await transport.set<any>('/foo', { bar: 'baz' });
    expect(result).toEqual({ id: '123' });
    expect(mockAxios.put).toHaveBeenCalledWith('/api/v1/databases/test-db/documents/foo', { doc: { bar: 'baz' } });
  });

  it('should update resource', async () => {
    const mockAxios = {
      patch: mock(async () => ({ data: { id: '123' } })),
    } as any;
    const transport = new RestTransport(mockAxios, TEST_DB);
    const result = await transport.update<any>('/foo', { bar: 'baz' });
    expect(result).toEqual({ id: '123' });
    expect(mockAxios.patch).toHaveBeenCalledWith('/api/v1/databases/test-db/documents/foo', { doc: { bar: 'baz' } });
  });

  it('should delete resource', async () => {
    const mockAxios = {
      delete: mock(async () => ({})),
    } as any;
    const transport = new RestTransport(mockAxios, TEST_DB);
    await transport.delete('/foo');
    expect(mockAxios.delete).toHaveBeenCalledWith('/api/v1/databases/test-db/documents/foo', {});
  });

  it('should query resources', async () => {
    const mockAxios = {
      post: mock(async () => ({ data: { documents: [{ type: 'object', value: { id: { type: 'string', value: '1' } } }], nextCursor: null, effectiveOrder: [] } })),
    } as any;
    const transport = new RestTransport(mockAxios, TEST_DB);
    const result = await transport.query('/api/v1/query', {});
    expect(result).toEqual([{ id: '1' }]);
    expect(mockAxios.post).toHaveBeenCalledWith('/api/v1/databases/test-db/query', {});
  });

  it('should reject a legacy raw array response', async () => {
    const transport = new RestTransport({
      post: mock(async () => ({ data: [{ id: '1' }] })),
    } as any, TEST_DB);
    await expect(transport.query('/api/v1/query', {})).rejects.toThrow('page');
  });
});
