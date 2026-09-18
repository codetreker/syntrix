import type { NamespaceTuple } from './storage-types.js';
export type { NamespaceTuple } from './storage-types.js';

const decodeSegment = (segment: string): unknown => {
  if (!/^[A-Za-z0-9_-]+$/.test(segment) || segment.length % 4 === 1) throw new Error('Malformed replica identity token');
  const binary = atob(segment.replace(/-/g, '+').replace(/_/g, '/'));
  return JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(binary, c => c.charCodeAt(0))));
};

export const parseReplicaSubject = (token: string | null): string => {
  if (!token) throw new Error('A JWT identity is required to open replica storage');
  const parts = token.split('.');
  if (parts.length !== 3 || !/^[A-Za-z0-9_-]+$/.test(parts[2])) throw new Error('Malformed replica identity token');
  const header = decodeSegment(parts[0]);
  const payload = decodeSegment(parts[1]);
  if (!header || typeof header !== 'object' || Array.isArray(header) ||
      !payload || typeof payload !== 'object' || Array.isArray(payload)) throw new Error('Malformed replica identity token');
  const claims = payload as Record<string, unknown>;
  if (typeof claims.sub !== 'string' || !claims.sub.trim()) throw new Error('Replica identity requires a nonempty subject');
  if ('oid' in claims && claims.oid !== claims.sub) throw new Error('Replica identity oid must equal sub');
  // Expiry is enforced by the server; it must not prevent offline access to this namespace.
  return claims.sub;
};

export const createNamespace = async (
  endpoint: string, database: string, name: string, alias: string, subject: string,
): Promise<{ tuple: NamespaceTuple; hash: string }> => {
  for (const value of [database, name, alias, subject]) if (!value.trim()) throw new Error('Replica namespace fields must be nonempty');
  const url = new URL(endpoint);
  if (!['https:', 'http:'].includes(url.protocol) || url.search || url.hash || url.username || url.password || /[?#]/.test(endpoint)) {
    throw new Error('Replica endpoint must be an HTTP URL without credentials, query, or fragment');
  }
  const canonical = url.href.replace(/\/+$/, '');
  const tuple: NamespaceTuple = { endpoint: canonical, subject, database, name, alias };
  const bytes = new TextEncoder().encode(JSON.stringify([canonical, subject, database, name, alias]));
  const digest = await crypto.subtle.digest('SHA-256', bytes);
  const hash = Array.from(new Uint8Array(digest), value => value.toString(16).padStart(2, '0')).join('');
  return { tuple, hash };
};
