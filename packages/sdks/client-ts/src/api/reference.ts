import { StorageClient } from '../internal/storage-client';
import { CollectionReference, DocumentReference, Query, FilterOp, QueryPage, QueryOrder } from './types';
import { encodeQueryValue } from './value';

export class DocumentReferenceImpl<T> implements DocumentReference<T> {
  protected _ifMatch: any[] = [];

  constructor(private storage: StorageClient, public path: string, public id: string) {}

  ifMatch(field: string, op: FilterOp, value: any): DocumentReference<T> {
    const ref = new DocumentReferenceImpl<T>(this.storage, this.path, this.id);
    ref._ifMatch = [...this._ifMatch, { field, op, value }];
    return ref;
  }

  async get(): Promise<T | null> {
    return this.storage.get<T>(this.path);
  }

  async set(data: T, ifMatch?: any[]): Promise<T> {
    const conditions = [...this._ifMatch, ...(ifMatch || [])];
    return this.storage.set<T>(this.path, data, conditions.length > 0 ? conditions : undefined);
  }

  async update(data: Partial<T>, ifMatch?: any[]): Promise<T> {
    const conditions = [...this._ifMatch, ...(ifMatch || [])];
    return this.storage.update<T>(this.path, data, conditions.length > 0 ? conditions : undefined);
  }

  async delete(ifMatch?: any[]): Promise<void> {
    const conditions = [...this._ifMatch, ...(ifMatch || [])];
    return this.storage.delete(this.path, conditions.length > 0 ? conditions : undefined);
  }

  collection<U>(path: string): CollectionReference<U> {
    return new CollectionReferenceImpl<U>(this.storage, `${this.path}/${path}`);
  }
}

export class QueryImpl<T> implements Query<T> {
  protected filters: { field: string; op: FilterOp; value: unknown }[] = [];
  protected sort: QueryOrder[] = [];
  protected limitVal?: number;
  protected startAfterVal?: string;
  protected showDeletedVal?: boolean;

  constructor(protected storage: StorageClient, public path: string) {}

  where(field: string, op: FilterOp, value: any): Query<T> {
    this.filters.push({ field, op, value });
    return this;
  }

  orderBy(field: string, direction: 'asc' | 'desc' = 'asc'): Query<T> {
    this.sort.push({ field, direction });
    return this;
  }

  limit(n: number): Query<T> {
    this.limitVal = n;
    return this;
  }

  startAfter(cursor: string): Query<T> {
    this.startAfterVal = cursor;
    return this;
  }

  showDeleted(show: boolean = true): Query<T> {
    this.showDeletedVal = show;
    return this;
  }

  async get(): Promise<T[]> {
    return (await this.getPage()).documents;
  }

  async getPage(): Promise<QueryPage<T>> {
    const query: any = {
      collection: this.path,
      filters: this.filters.map(filter => ({ ...filter, value: encodeQueryValue(filter.value) })),
      orderBy: this.sort,
    };
    if (this.limitVal !== undefined) {
      query.limit = this.limitVal;
    }
    if (this.startAfterVal !== undefined) {
      query.startAfter = this.startAfterVal;
    }
    if (this.showDeletedVal !== undefined) {
      query.showDeleted = this.showDeletedVal;
    }
    return this.storage.queryPage<T>('/api/v1/query', query);
  }

  async update(data: Partial<T>): Promise<void> {
    const docs = await this.get();
    const promises = docs.map((doc: any) => {
      if (doc.id) {
        return this.storage.update(`${this.path}/${doc.id}`, data);
      }
      return Promise.resolve();
    });
    await Promise.all(promises);
  }

  async delete(): Promise<void> {
    const docs = await this.get();
    const promises = docs.map((doc: any) => {
      if (doc.id) {
        return this.storage.delete(`${this.path}/${doc.id}`);
      }
      return Promise.resolve();
    });
    await Promise.all(promises);
  }
}

export class CollectionReferenceImpl<T> extends QueryImpl<T> implements CollectionReference<T> {
  constructor(storage: StorageClient, path: string) {
    super(storage, path);
  }

  doc(id?: string): DocumentReference<T> {
    if (id) {
      return new DocumentReferenceImpl<T>(this.storage, `${this.path}/${id}`, id);
    }
    const autoId = crypto.randomUUID();
    return new DocumentReferenceImpl<T>(this.storage, `${this.path}/${autoId}`, autoId);
  }

  async add(data: T): Promise<DocumentReference<T>> {
    const result: any = await this.storage.create<T>(this.path, data);
    const id = result.id;
    if (!id) {
        throw new Error('Server did not return an ID for the created document');
    }
    return new DocumentReferenceImpl<T>(this.storage, `${this.path}/${id}`, id);
  }
}
