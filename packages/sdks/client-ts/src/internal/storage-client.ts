import { QueryPage } from '../api/types';

export interface StorageClient {
  get<T>(path: string): Promise<T | null>;
  create<T>(path: string, data: T): Promise<T>;
  set<T>(path: string, data: T, ifMatch?: any[]): Promise<T>;
  update<T>(path: string, data: Partial<T>, ifMatch?: any[]): Promise<T>;
  delete(path: string, ifMatch?: any[]): Promise<void>;
  query<T>(path: string, query: any): Promise<T[]>;
  queryPage<T>(path: string, query: any): Promise<QueryPage<T>>;
}
