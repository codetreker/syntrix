import { api } from './api';

export interface Document {
  id: string;
  _collection?: string;
  _createdAt?: string;
  _updatedAt?: string;
  [key: string]: unknown;
}

export interface Filter {
  field: string;
  op: 'eq' | 'ne' | 'gt' | 'gte' | 'lt' | 'lte' | 'in' | 'contains';
  value: unknown;
}

export interface QueryOptions {
  database: string;
  collection: string;
  filters?: Filter[];
  limit?: number;
  startAfter?: string;
  orderBy?: { field: string; dir: 'asc' | 'desc' }[];
}

export interface QueryResponse {
  documents: Document[];
  hasMore: boolean;
}

export interface CollectionInfo {
  name: string;
  count?: number;
}

export const documentsApi = {
  /**
   * Query documents using POST /api/v1/databases/{db}/query
   */
  query: async (options: QueryOptions): Promise<QueryResponse> => {
    const limit = options.limit || 20;
    const response = await api.post<Document[]>(
      `/api/v1/databases/${encodeURIComponent(options.database)}/query`,
      {
        collection: options.collection,
        filters: options.filters || [],
        limit: limit + 1,
        startAfter: options.startAfter,
        orderBy: options.orderBy || [],
      }
    );

    const docs = response.data || [];
    const hasMore = docs.length > limit;

    return {
      documents: hasMore ? docs.slice(0, limit) : docs,
      hasMore,
    };
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
      const response = await api.post<Document[]>(
        `/api/v1/databases/${encodeURIComponent(database)}/query`,
        { collection: '', filters: [], limit: 100, orderBy: [] }
      );
      const docs = response.data || [];
      const collectionSet = new Set<string>();
      for (const doc of docs) {
        if (doc._collection) {
          collectionSet.add(doc._collection);
        }
      }
      return Array.from(collectionSet).sort().map((name) => ({ name }));
    } catch {
      return [];
    }
  },
};
