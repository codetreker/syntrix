import type { AxiosInstance, AxiosRequestConfig } from 'axios';
import { AuthSessionChangedError } from '../../api/errors.js';
import type { TokenProvider } from '../auth/types.js';
import { ReplicaStorageError, type FrozenSourceDefinition } from './storage-types.js';
import type { ReplicaSourceAdapter } from './source-types.js';
import { copySourceDefinition, decodeSourceResponse, encodeSourceRequest, parseSourceEnvelope, sourcePageBytes, validateSourceDatabase } from './source-codec.js';

export { decodeSourceResponse, sourcePageBytes, sourcePageCount } from './source-codec.js';

export const createReplicaHttpSource = (options: {
  axios: AxiosInstance;
  provider: TokenProvider;
  database: string;
  definition: FrozenSourceDefinition;
}): ReplicaSourceAdapter => {
  validateSourceDatabase(options.database);
  const definition = copySourceDefinition(options.definition);
  const mode = definition.limit === null ? 'events' : 'replace';
  return {
    definition, mode,
    async read(context) {
      context.signal.throwIfAborted();
      if (options.provider.getSessionVersion() !== context.sessionVersion) throw new AuthSessionChangedError();
      const body = encodeSourceRequest(definition, context);
      const config: AxiosRequestConfig & { _syntrixAuthSessionVersion: number } = {
        signal: context.signal,
        _syntrixAuthSessionVersion: context.sessionVersion,
        responseType: 'text',
        maxContentLength: sourcePageBytes,
        // Parse error envelopes here too, before authentication/error interceptors
        // consume them, so RESYNC_REQUIRED and identity errors retain their codes.
        transformResponse: [(data: unknown, _headers: unknown, status?: number) => {
          if (status !== undefined && (status < 200 || status >= 300)) {
            if (typeof data === 'string' && data.length === 0) return data;
            try { return parseSourceEnvelope(data); } catch (error) {
              if (error instanceof ReplicaStorageError && error.code === 'ReplicaSourceResponseTooLarge') throw error;
              return data;
            }
          }
          return data;
        }],
        headers: context.expectedDatabaseIdentity === null ? {} : {
          'X-Syntrix-Expected-Database-Identity': context.expectedDatabaseIdentity,
        },
      };
      const response = await options.axios.post(
        `/replication/v1/databases/${encodeURIComponent(options.database)}/pull`, body, config,
      );
      context.signal.throwIfAborted();
      if (options.provider.getSessionVersion() !== context.sessionVersion) throw new AuthSessionChangedError();
      return decodeSourceResponse(response.data, definition, context);
    },
  };
};
