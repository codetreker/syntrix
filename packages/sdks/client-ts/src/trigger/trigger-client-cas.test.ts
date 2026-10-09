import { describe, it, expect, mock, beforeEach, afterEach } from 'bun:test';
import { TriggerClient } from './trigger-client';
import axios from 'axios';

// Mock axios
const originalCreate = axios.create;
let mockAxiosInstance: any;

describe('TriggerClient CAS Operations', () => {
  beforeEach(() => {
    mockAxiosInstance = {
      post: mock(async () => ({ data: {} })),
    };
    axios.create = mock(() => mockAxiosInstance);
  });

  afterEach(() => {
    axios.create = originalCreate;
  });

  it('should support batch write with ifMatch', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    const writes = [{
      type: 'update',
      path: 'users/1',
      data: { age: 30 },
      ifMatch: [{ field: 'version', op: '==', value: 1 }]
    }] as any;

    await client.batch(writes);

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', { writes });
  });

  it('should support batch update with multiple ifMatch conditions', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    const writes = [{
      type: 'update',
      path: 'users/1',
      data: { age: 30 },
      ifMatch: [
        { field: 'version', op: '==', value: 1 },
        { field: 'status', op: '==', value: 'active' }
      ]
    }] as any;

    await client.batch(writes);

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', { writes });
  });

  it('should support batch delete with multiple ifMatch conditions', async () => {
    const client = new TriggerClient('http://localhost', 'token');
    const writes = [{
      type: 'delete',
      path: 'users/1',
      ifMatch: [
        { field: 'version', op: '==', value: 5 },
        { field: 'locked', op: '==', value: false }
      ]
    }] as any;

    await client.batch(writes);

    expect(mockAxiosInstance.post).toHaveBeenCalledWith('/write', { writes });
  });
});
