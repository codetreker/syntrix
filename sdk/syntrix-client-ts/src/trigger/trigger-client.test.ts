import { describe, it, expect, mock, beforeEach, afterEach } from 'bun:test';
import { TriggerClient } from './trigger-client';
import axios from 'axios';

// Mock axios
const originalCreate = axios.create;
let mockAxiosInstance: any;

describe('TriggerClient', () => {
  beforeEach(() => {
    mockAxiosInstance = {
      post: mock(async () => ({ data: {} })),
      get: mock(async () => ({ data: {} })),
      put: mock(async () => ({ data: {} })),
      patch: mock(async () => ({ data: {} })),
      delete: mock(async () => ({ data: {} })),
    };
    axios.create = mock(() => mockAxiosInstance);
  });

  afterEach(() => {
    axios.create = originalCreate;
  });

  it('should initialize with correct baseURL and headers', () => {
    new TriggerClient('http://api.synbase.tech', 'my-token');
    expect(axios.create).toHaveBeenCalledWith({
      baseURL: 'http://api.synbase.tech/trigger/v1/databases/default',
      headers: { Authorization: 'Bearer my-token' }
    });
  });

  it('should initialize with custom database', () => {
    new TriggerClient('http://api.synbase.tech', 'my-token', 'my-database');
    expect(axios.create).toHaveBeenCalledWith({
      baseURL: 'http://api.synbase.tech/trigger/v1/databases/my-database',
      headers: { Authorization: 'Bearer my-token' }
    });
  });

  it('should send batch writes to /write', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    const writes = [{ type: 'create', path: 'users/1', data: { name: 'Alice' } }] as any;

    await client.batch(writes);

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', { writes });
  });

  it('should map get() to POST /get', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockResolvedValue({ data: { docs: [{ id: '1', name: 'Alice' }] } });

    const result = await client.get('users/1');

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/get', { path: 'users/1' });
    expect(result).toEqual({ id: '1', name: 'Alice' });
  });

  it('should return null from get() if docs is empty', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockResolvedValue({ data: { docs: [] } });

    const result = await client.get('users/1');
    expect(result).toBeNull();
  });

  it('should return null from get() on 404', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockRejectedValue({ response: { status: 404 } });

    const result = await client.get('users/1');
    expect(result).toBeNull();
  });

  it('should throw on create() without ID', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    try {
      await client.create('users', { name: 'Bob' });
      expect(true).toBe(false); // Should not reach here
    } catch (e: any) {
      expect(e.message).toContain('TriggerClient requires explicit ID');
    }
  });

  it('should map createWithId() to batch create', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    await client.createWithId('users', { name: 'Bob' }, 'bob-id');

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'create', path: 'users/bob-id', data: { name: 'Bob' } }]
    });
  });

  it('should map set() to batch set', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    await client.set('users/1', { name: 'Alice' });

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'set', path: 'users/1', data: { name: 'Alice' } }]
    });
  });

  it('should map update() to batch update', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    await client.update('users/1', { age: 30 });

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'update', path: 'users/1', data: { age: 30 } }]
    });
  });

  it('should map delete() to batch delete', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    await client.delete('users/1');

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'delete', path: 'users/1' }]
    });
  });

  it('should map query() to POST /query', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    const query = { collection: 'users', filters: [] };
    mockAxiosInstance.post.mockResolvedValue({ data: { documents: [{ type: 'object', value: { id: { type: 'string', value: '1' } } }], nextCursor: null, effectiveOrder: [] } });

    // The path argument is ignored by TriggerClient implementation
    const result = await client.query('/api/v1/query', query);

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/query', query);
    expect(result).toEqual([{ id: '1' }]);
  });

  it('should throw when calling collection().add()', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    const col = client.collection('users');
    try {
      await col.add({ name: 'Fail' });
      expect(true).toBe(false);
    } catch (e: any) {
      expect(e.message).toContain('TriggerClient requires explicit ID');
    }
  });

  it('should support doc().set() via reference', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    const doc = client.doc('users/1');
    await doc.set({ name: 'Alice' });

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'set', path: 'users/1', data: { name: 'Alice' } }]
    });
  });

  it('should support chainable doc().update()', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    await client.collection('users').doc('1').update({ age: 31 });

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'update', path: 'users/1', data: { age: 31 } }]
    });
  });

  it('should support chainable doc().delete()', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    await client.collection('users').doc('1').delete();

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'delete', path: 'users/1' }]
    });
  });

  it('should support chainable doc().get()', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockResolvedValue({ data: { docs: [{ id: '1', name: 'Alice' }] } });

    const doc = await client.collection('users').doc('1').get();

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/get', { path: 'users/1' });
    expect(doc).toEqual({ id: '1', name: 'Alice' });
  });

  it('should support chainable query via collection()', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockResolvedValue({ data: { documents: [{ type: 'object', value: { id: { type: 'string', value: '1' } } }], nextCursor: null, effectiveOrder: [] } });

    await client.collection('users')
      .where('age', '>', 18)
      .orderBy('age', 'desc')
      .limit(10)
      .get();

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/query', {
      collection: 'users',
      filters: [{ field: 'age', op: '>', value: { type: 'float64', value: 18 } }],
      orderBy: [{ field: 'age', direction: 'desc' }],
      limit: 10
    });
  });
});
