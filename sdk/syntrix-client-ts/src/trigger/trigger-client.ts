import axios, { AxiosInstance } from 'axios';
import { StorageClient } from '../internal/storage-client';
import { CollectionReference, DocumentReference, QueryPage } from '../api/types';
import { CollectionReferenceImpl, DocumentReferenceImpl } from '../api/reference';
import { decodeQueryPage } from '../internal/query-page';

export interface WriteOp {
  type: 'create' | 'update' | 'set' | 'delete';
  path: string;
  data?: any;
  ifMatch?: any[]; // Array of filters for CAS
}

class TriggerCollectionReferenceImpl<T> extends CollectionReferenceImpl<T> {
  async add(data: T): Promise<DocumentReference<T>> {
      throw new Error('TriggerClient requires explicit ID for creation. Use doc(id).set(data) or create(collection, data, id) instead.');
  }
}

export class TriggerClient implements StorageClient {
  private axios: AxiosInstance;
  private database: string;

  constructor(baseURL: string, token: string, database: string = 'default') {
    const cleanBaseUrl = baseURL.replace(/\/$/, '');
    this.database = database;
    this.axios = axios.create({
        baseURL: `${cleanBaseUrl}/trigger/v1/databases/${database}`,
        headers: { Authorization: `Bearer ${token}` }
    });
  }

  getDatabase(): string {
    return this.database;
  }

  /**
   * Performs an atomic batch write.
   * Maps to POST /trigger/v1/databases/{database}/write
   */
  async batch(writes: WriteOp[]): Promise<void> {
      await this.axios.post('/write', { writes });
  }

  // --- StorageClient Implementation ---

  async get<T>(path: string): Promise<T | null> {
      try {
          // Maps to POST /trigger/v1/databases/{database}/get
          const res = await this.axios.post('/get', { path });

          // Tolerant to empty responses or array wrapper
          if (res.data && Array.isArray(res.data.docs) && res.data.docs.length > 0) {
              return res.data.docs[0];
          }
          // If the server returns the document directly (unlikely for this endpoint but possible)
          if (res.data && !Array.isArray(res.data) && !res.data.docs) {
             return res.data;
          }
          return null;
      } catch (err: any) {
          if (err.response?.status === 404) return null;
          throw err;
      }
  }

  async create<T>(path: string, data: T): Promise<T> {
      // StorageClient.create is typically used for auto-ID generation (collection.add).
      // TriggerClient forbids this.
      throw new Error('TriggerClient requires explicit ID. Use create(collection, data, id) or doc(id).set(data).');
  }

  async set<T>(path: string, data: T, ifMatch?: any[]): Promise<T> {
      await this.batch([{ type: 'set', path, data, ifMatch }]);
      return data;
  }

  async update<T>(path: string, data: Partial<T>, ifMatch?: any[]): Promise<T> {
      await this.batch([{ type: 'update', path, data, ifMatch }]);
      return data as T;
  }

  async delete(path: string, ifMatch?: any[]): Promise<void> {
      await this.batch([{ type: 'delete', path, ifMatch }]);
  }

  async query<T>(path: string, query: any): Promise<T[]> {
      return (await this.queryPage<T>(path, query)).documents;
  }

  async queryPage<T>(_path: string, query: any): Promise<QueryPage<T>> {
      const res = await this.axios.post('/query', query);
      return decodeQueryPage<T>(res.data);
  }

  // --- Public API Extensions ---

  /**
   * Convenience method for creating a document with a specific ID.
   */
  async createWithId<T>(collectionPath: string, data: T, id: string): Promise<T> {
      const path = `${collectionPath}/${id}`;
      await this.batch([{ type: 'create', path, data }]);
      return data;
  }

  collection<T>(path: string): CollectionReference<T> {
    return new TriggerCollectionReferenceImpl<T>(this, path);
  }

  doc<T>(path: string): DocumentReference<T> {
      const parts = path.split('/');
      const id = parts[parts.length - 1];
      return new DocumentReferenceImpl<T>(this, path, id);
  }
}
