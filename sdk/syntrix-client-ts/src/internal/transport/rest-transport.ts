import { AxiosInstance } from 'axios';
import { StorageClient } from '../storage-client';
import { QueryPage } from '../../api/types';
import { decodeQueryPage } from '../query-page';

export class RestTransport implements StorageClient {
  private database: string;

  constructor(private axios: AxiosInstance, database: string) {
    this.database = database;
  }

  private buildPath(path: string): string {
    // If path already starts with /api/v1, use as-is (for query endpoint)
    if (path.startsWith('/api/v1')) {
      // Insert database into the path if it's a query endpoint
      if (path === '/api/v1/query') {
        return `/api/v1/databases/${this.database}/query`;
      }
      return path;
    }
    // Build document path: /api/v1/databases/{database}/documents/{path}
    const cleanPath = path.startsWith('/') ? path.slice(1) : path;
    return `/api/v1/databases/${this.database}/documents/${cleanPath}`;
  }

  async get<T>(path: string): Promise<T | null> {
    try {
      const response = await this.axios.get(this.buildPath(path));
      return response.data;
    } catch (error: any) {
      if (error.response && error.response.status === 404) {
        return null;
      }
      throw error;
    }
  }

  async create<T>(path: string, data: T): Promise<T> {
    const response = await this.axios.post(this.buildPath(path), data);
    return response.data;
  }

  async set<T>(path: string, data: T, ifMatch?: any[]): Promise<T> {
    const payload: any = { doc: data };
    if (ifMatch) {
      payload.ifMatch = ifMatch;
    }
    const response = await this.axios.put(this.buildPath(path), payload);
    return response.data;
  }

  async update<T>(path: string, data: Partial<T>, ifMatch?: any[]): Promise<T> {
    const payload: any = { doc: data };
    if (ifMatch) {
      payload.ifMatch = ifMatch;
    }
    const response = await this.axios.patch(this.buildPath(path), payload);
    return response.data;
  }

  async delete(path: string, ifMatch?: any[]): Promise<void> {
    const config: any = {};
    if (ifMatch) {
      config.data = { ifMatch };
    }
    await this.axios.delete(this.buildPath(path), config);
  }

  async query<T>(path: string, query: any): Promise<T[]> {
    return (await this.queryPage<T>(path, query)).documents;
  }

  async queryPage<T>(path: string, query: any): Promise<QueryPage<T>> {
    const response = await this.axios.post(this.buildPath(path), query);
    return decodeQueryPage<T>(response.data);
  }
}
