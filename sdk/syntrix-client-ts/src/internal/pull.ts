import type { AxiosInstance, AxiosRequestConfig } from 'axios';
import { AuthSessionChangedError } from '../api/errors';
import type { PullDocument, PullOptions, PullPage } from '../api/types';
import { decodeQueryValue } from '../api/value';
import type { TokenProvider } from './auth/types';

const maxCheckpointBytes = 256 * 1024;
const metadataFields = ['version', 'createdAt', 'updatedAt'] as const;

const validateCheckpoint = (checkpoint: unknown): string => {
  if (typeof checkpoint !== 'string' || checkpoint.length === 0 ||
      new TextEncoder().encode(checkpoint).length > maxCheckpointBytes) {
    throw new TypeError('Pull checkpoint must be a nonempty string of at most 256 KiB');
  }
  return checkpoint;
};

export const decodePullPage = <T>(value: unknown, collection: string, limit: number): PullPage<T> => {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) {
    throw new TypeError('Pull response must be a page');
  }
  const page = value as Record<string, unknown>;
  if (!Array.isArray(page.documents) || page.documents.length > limit || typeof page.caughtUp !== 'boolean') {
    throw new TypeError('Pull page requires bounded documents and a boolean caughtUp');
  }
  const checkpoint = validateCheckpoint(page.checkpoint);
  const documents = page.documents.map(node => {
    const document = decodeQueryValue(node);
    if (document === null || typeof document !== 'object' || Array.isArray(document) ||
        typeof document.id !== 'string' || document.id.length === 0 || /[/\u0000]/.test(document.id) ||
        document.collection !== collection ||
        ('deleted' in document && typeof document.deleted !== 'boolean')) {
      throw new TypeError('Pull document requires a logical ID, matching collection, and valid deletion metadata');
    }
    for (const field of metadataFields) {
      if ((field in document || document.deleted !== true) && typeof document[field] !== 'bigint') {
        throw new TypeError(`Pull document ${field} must be int64 metadata`);
      }
    }
    return document as PullDocument<T>;
  });
  return { documents, checkpoint, caughtUp: page.caughtUp };
};

export class PullTransport {
  constructor(
    private axios: AxiosInstance,
    private provider: TokenProvider,
    private database: string,
  ) {}

  async pull<T>(collection: string, options: PullOptions = {}): Promise<PullPage<T>> {
    if (typeof collection !== 'string' || collection.length === 0) {
      throw new TypeError('Pull collection must be a nonempty string');
    }
    const checkpoint = options.checkpoint === undefined || options.checkpoint === null
      ? null : validateCheckpoint(options.checkpoint);
    const limit = options.limit === undefined ? 100 : options.limit;
    if (!Number.isInteger(limit) || limit < 1 || limit > 1000) {
      throw new RangeError('Pull limit must be an integer between 1 and 1000');
    }
    const sessionVersion = this.provider.getSessionVersion();
    // Bind before Axios schedules its asynchronous request interceptor.
    const config: AxiosRequestConfig & { _syntrixAuthSessionVersion: number } = {
      signal: options.signal,
      _syntrixAuthSessionVersion: sessionVersion,
    };
    const response = await this.axios.post(
      `/replication/v1/databases/${encodeURIComponent(this.database)}/pull`,
      { collection, checkpoint, limit },
      config,
    );
    if (this.provider.getSessionVersion() !== sessionVersion) throw new AuthSessionChangedError();
    return decodePullPage<T>(response.data, collection, limit);
  }
}
