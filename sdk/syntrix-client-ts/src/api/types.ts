export type FilterOp = '==' | '!=' | '>' | '>=' | '<' | '<=' | 'in' | 'contains';

export interface QueryOrder {
  field: string;
  direction: 'asc' | 'desc';
}

export interface QueryPage<T> {
  documents: T[];
  nextCursor: string | null;
  effectiveOrder: QueryOrder[];
}

export interface DocumentReference<T> {
  id: string;
  path: string;
  get(): Promise<T | null>;
  ifMatch(field: string, op: FilterOp, value: any): DocumentReference<T>;
  set(data: T, ifMatch?: any[]): Promise<T>;
  update(data: Partial<T>, ifMatch?: any[]): Promise<T>;
  delete(ifMatch?: any[]): Promise<void>;
  collection<U>(path: string): CollectionReference<U>;
}

export interface Query<T> {
  where(field: string, op: FilterOp, value: any): Query<T>;
  orderBy(field: string, direction?: 'asc' | 'desc'): Query<T>;
  limit(n: number): Query<T>;
  startAfter(cursor: string): Query<T>;
  showDeleted(show?: boolean): Query<T>;
  getPage(): Promise<QueryPage<T>>;
  /** Returns the selected page. Use getPage() to retain its continuation cursor. */
  get(): Promise<T[]>;
  /** Updates only documents in the selected page. */
  update(data: Partial<T>): Promise<void>;
  /** Deletes only documents in the selected page. */
  delete(): Promise<void>;
}

export interface CollectionReference<T> extends Query<T> {
  path: string;
  doc(id?: string): DocumentReference<T>;
  add(data: T): Promise<DocumentReference<T>>;
}
