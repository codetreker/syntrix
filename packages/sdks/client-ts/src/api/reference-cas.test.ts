import { describe, it, expect, mock, beforeEach } from 'bun:test';
import { DocumentReferenceImpl } from './reference';
import { StorageClient } from '../internal/storage-client';

describe('Reference API CAS', () => {
  let mockStorage: StorageClient;

  beforeEach(() => {
    mockStorage = {
      get: mock(async () => null),
      create: mock(async () => ({})),
      set: mock(async () => ({})),
      update: mock(async () => ({})),
      delete: mock(async () => undefined),
      query: mock(async () => []),
    } as unknown as StorageClient;
  });

  it('should pass ifMatch to storage.set', async () => {
    const doc = new DocumentReferenceImpl(mockStorage, 'users/1', '1');
    const ifMatch = [{ field: 'version', op: '==', value: 1 }];

    await doc.set({ name: 'Alice' }, ifMatch);

    expect(mockStorage.set).toHaveBeenCalledWith('users/1', { name: 'Alice' }, ifMatch);
  });

  it('should pass ifMatch to storage.update', async () => {
    const doc = new DocumentReferenceImpl(mockStorage, 'users/1', '1');
    const ifMatch = [{ field: 'version', op: '==', value: 1 }];

    await doc.update({ age: 30 }, ifMatch);

    expect(mockStorage.update).toHaveBeenCalledWith('users/1', { age: 30 }, ifMatch);
  });

  it('should pass ifMatch to storage.delete', async () => {
    const doc = new DocumentReferenceImpl(mockStorage, 'users/1', '1');
    const ifMatch = [{ field: 'version', op: '==', value: 1 }];

    await doc.delete(ifMatch);

    expect(mockStorage.delete).toHaveBeenCalledWith('users/1', ifMatch);
  });

  it('should support chainable ifMatch syntax', async () => {
    const doc = new DocumentReferenceImpl(mockStorage, 'users/1', '1');

    await doc.ifMatch('version', '==', 1).update({ age: 31 });

    expect(mockStorage.update).toHaveBeenCalledWith(
      'users/1',
      { age: 31 },
      [{ field: 'version', op: '==', value: 1 }]
    );
  });

  it('should support multiple chainable ifMatch calls', async () => {
    const doc = new DocumentReferenceImpl(mockStorage, 'users/1', '1');

    await doc
      .ifMatch('version', '==', 1)
      .ifMatch('status', '==', 'active')
      .delete();

    expect(mockStorage.delete).toHaveBeenCalledWith(
      'users/1',
      [
        { field: 'version', op: '==', value: 1 },
        { field: 'status', op: '==', value: 'active' }
      ]
    );
  });

  it('should ensure ifMatch is immutable', async () => {
    const doc = new DocumentReferenceImpl(mockStorage, 'users/1', '1');
    const docWithCondition = doc.ifMatch('version', '==', 1);

    // Original doc should not have conditions
    await doc.update({ age: 30 });
    expect(mockStorage.update).toHaveBeenCalledWith('users/1', { age: 30 }, undefined);

    // New doc should have conditions
    await docWithCondition.update({ age: 31 });
    expect(mockStorage.update).toHaveBeenCalledWith(
      'users/1',
      { age: 31 },
      [{ field: 'version', op: '==', value: 1 }]
    );
  });
});
