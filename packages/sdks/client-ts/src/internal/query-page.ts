import type { QueryOrder, QueryPage } from '../api/types';
import { decodeQueryValue } from '../api/value';

export const decodeQueryPage = <T>(value: unknown): QueryPage<T> => {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new TypeError('Query response must be a page');
  }
  const page = value as Record<string, unknown>;
  if (!Array.isArray(page.documents) || !Array.isArray(page.effectiveOrder) ||
      (page.nextCursor !== null && typeof page.nextCursor !== 'string')) {
    throw new TypeError('Query page requires documents, nextCursor, and effectiveOrder');
  }
  const effectiveOrder = page.effectiveOrder.map((entry): QueryOrder => {
    if (typeof entry !== 'object' || entry === null || Array.isArray(entry) ||
        typeof entry.field !== 'string' || (entry.direction !== 'asc' && entry.direction !== 'desc')) {
      throw new TypeError('Invalid query page order');
    }
    return { field: entry.field, direction: entry.direction };
  });
  const documents = page.documents.map(node => {
    const document = decodeQueryValue(node);
    if (document === null || typeof document !== 'object' || Array.isArray(document)) {
      throw new TypeError('Query document must be a typed object');
    }
    return document as T;
  });
  return { documents, nextCursor: page.nextCursor, effectiveOrder };
};
