import { api, dataApi, type QueryRequest } from './api';
import type { FilterOp, QueryPage } from '../../../sdk/syntrix-client-ts/src/api/types';
import { encodeQueryValue } from '../../../sdk/syntrix-client-ts/src/api/value';

export interface Document {
  id: string;
  _collection?: string;
  _createdAt?: string;
  _updatedAt?: string;
  [key: string]: unknown;
}

export interface Filter {
  field: string;
  op: FilterOp;
  value: unknown;
}

export type QueryOptions = QueryRequest;
export type QueryResponse = QueryPage<Document>;

export interface CollectionInfo {
  name: string;
  count?: number;
}

export const documentsApi = {
  /**
   * Query documents using POST /api/v1/databases/{db}/query
   */
  query: async (options: QueryOptions): Promise<QueryResponse> => {
    return dataApi.query({ limit: 20, orderBy: [], ...options });
  },

  /**
   * Get a single document: GET /api/v1/databases/{db}/documents/{collection}/{id}
   */
  get: async (database: string, collection: string, id: string): Promise<Document> => {
    const response = await api.get<Document>(
      `/api/v1/databases/${encodeURIComponent(database)}/documents/${collection}/${id}`
    );
    return response.data;
  },

  /**
   * Create a new document: POST /api/v1/databases/{db}/documents/{collection}
   */
  create: async (database: string, collection: string, data: Record<string, unknown>, documentId?: string): Promise<Document> => {
    const payload = documentId ? { ...data, id: documentId } : data;
    const response = await api.post<Document>(
      `/api/v1/databases/${encodeURIComponent(database)}/documents/${collection}`,
      payload
    );
    return response.data;
  },

  /**
   * Update (patch) an existing document: PATCH /api/v1/databases/{db}/documents/{collection}/{id}
   * Backend expects { doc: {...} }
   */
  update: async (database: string, collection: string, id: string, data: Record<string, unknown>): Promise<Document> => {
    const response = await api.patch<Document>(
      `/api/v1/databases/${encodeURIComponent(database)}/documents/${collection}/${id}`,
      { doc: data }
    );
    return response.data;
  },

  /**
   * Delete a document: DELETE /api/v1/databases/{db}/documents/{collection}/{id}
   */
  delete: async (database: string, collection: string, id: string): Promise<void> => {
    await api.delete(
      `/api/v1/databases/${encodeURIComponent(database)}/documents/${collection}/${id}`
    );
  },

  /**
   * Discover collections by querying with an empty filter.
   * Since there's no dedicated collections endpoint, we look at _collection fields
   * from a broad query with a small limit.
   */
  listCollections: async (database: string): Promise<CollectionInfo[]> => {
    try {
      // Query with no collection filter to discover what exists
      // The backend may return documents from various collections
      const { documents: docs } = await dataApi.query({ database, collection: '', filters: [], limit: 100, orderBy: [] });
      const collectionSet = new Set<string>();
      for (const doc of docs) {
        if (typeof doc._collection === 'string') {
          collectionSet.add(doc._collection);
        }
      }
      return Array.from(collectionSet).sort().map((name) => ({ name }));
    } catch {
      return [];
    }
  },
};

export const parseFilterValue = (input: string): unknown => {
  let value: unknown = input;
  if (input === 'null') value = null;
  else if (input === 'true') value = true;
  else if (input === 'false') value = false;
  else if (/^-?(0|[1-9][0-9]*)$/.test(input)) value = BigInt(input);
  else if (/^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$/.test(input)) value = Number(input);
  encodeQueryValue(value);
  return value;
};

/** Serializes int64 values as exact JSON integer tokens for display and copying. */
export const formatDocumentJson = (value: unknown, indent = 2): string => {
  const format = (item: unknown, depth: number): string => {
    if (typeof item === 'bigint') return item.toString();
    if (item === null || typeof item !== 'object') {
      const encoded = JSON.stringify(item);
      if (encoded === undefined) throw new TypeError('Document contains an unsupported JSON value');
      return encoded;
    }
    const array = Array.isArray(item);
    const entries = array
      ? item.map(child => format(child, depth + 1))
      : Object.entries(item).map(([key, child]) => `${JSON.stringify(key)}:${indent ? ' ' : ''}${format(child, depth + 1)}`);
    const [open, close] = array ? ['[', ']'] : ['{', '}'];
    if (entries.length === 0) return open + close;
    if (!indent) return open + entries.join(',') + close;
    const padding = ' '.repeat(indent * (depth + 1));
    return `${open}\n${padding}${entries.join(`,\n${padding}`)}\n${' '.repeat(indent * depth)}${close}`;
  };
  return format(value, 0);
};

export const documentData = (document: Document): Record<string, unknown> => {
  const metadata = new Set(['id', 'collection', 'createdAt', 'updatedAt', 'version', 'deleted', '_collection', '_createdAt', '_updatedAt']);
  return Object.fromEntries(Object.entries(document).filter(([key]) => !metadata.has(key)));
};

export const hasInt64Value = (value: unknown): boolean => {
  if (typeof value === 'bigint') return true;
  if (value === null || typeof value !== 'object') return false;
  return Object.values(value).some(hasInt64Value);
};

export const formatDocumentDate = (value: unknown): string => {
  if (typeof value === 'bigint') {
    // The Date domain is within the exact-integer range of a JavaScript number.
    if (value < -8640000000000000n || value > 8640000000000000n) return '-';
    value = Number(value);
  }
  if (typeof value !== 'number' && typeof value !== 'string') return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return date.toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
};
