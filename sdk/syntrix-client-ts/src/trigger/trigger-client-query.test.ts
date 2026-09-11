import { describe, it, expect, mock, beforeEach, afterEach } from 'bun:test';
import { TriggerClient } from './trigger-client';
import axios from 'axios';
import { encodeQueryValue } from '../api/value';

// Mock axios
const originalCreate = axios.create;
let mockAxiosInstance: any;

describe('TriggerClient Query Operations', () => {
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

  it('should support chainable update by query', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockResolvedValue({ data: { documents: [encodeQueryValue({ id: '1' }), encodeQueryValue({ id: '2' })], nextCursor: 'next-page', effectiveOrder: [] } });

    await client.collection('users')
      .where('age', '>', 18)
      .startAfter('selected-page')
      .limit(2)
      .update({ active: true });

    // First, it queries
    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/query', {
      collection: 'users',
      filters: [{ field: 'age', op: '>', value: { type: 'float64', value: 18 } }],
      orderBy: [],
      startAfter: 'selected-page',
      limit: 2
    });

    expect(mockAxiosInstance.post).toHaveBeenCalledTimes(3);

    // Then it updates each doc
    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'update', path: 'users/1', data: { active: true } }]
    });
    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'update', path: 'users/2', data: { active: true } }]
    });
  });

  it('should support chainable delete by query', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockResolvedValue({ data: { documents: [encodeQueryValue({ id: '1' })], nextCursor: 'next-page', effectiveOrder: [] } });

    await client.collection('users')
      .where('status', '==', 'inactive')
      .delete();

    // First, it queries
    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/query', {
      collection: 'users',
      filters: [{ field: 'status', op: '==', value: { type: 'string', value: 'inactive' } }],
      orderBy: []
    });

    expect(mockAxiosInstance.post).toHaveBeenCalledTimes(2);

    // Then it deletes
    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', {
      writes: [{ type: 'delete', path: 'users/1' }]
    });
  });

  it('should support multiple where clauses', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    mockAxiosInstance.post.mockResolvedValue({ data: { documents: [], nextCursor: null, effectiveOrder: [] } });

    await client.collection('users')
      .where('age', '>', 18)
      .where('status', '==', 'active')
      .get();

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/query', {
      collection: 'users',
      filters: [
        { field: 'age', op: '>', value: { type: 'float64', value: 18 } },
        { field: 'status', op: '==', value: { type: 'string', value: 'active' } }
      ],
      orderBy: []
    });
  });
});
