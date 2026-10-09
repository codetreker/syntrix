import { AxiosInstance, AxiosError, CanceledError, InternalAxiosRequestConfig, isCancel } from 'axios';
import { TokenProvider } from './types';
import { AuthSessionChangedError, SyntrixError } from '../../api/errors';

type AuthRequestConfig = InternalAxiosRequestConfig & {
  _retry?: boolean;
  _syntrixAuthSessionVersion?: number;
};

const abortReason = (config: AuthRequestConfig): unknown =>
  (config.signal as (typeof config.signal & { readonly reason?: unknown }))?.reason;

const canceledRequest = (config: AuthRequestConfig) =>
  Object.assign(new CanceledError('canceled', config), { cause: abortReason(config) });

const waitForCredentials = <T>(config: AuthRequestConfig, read: () => Promise<T>): Promise<T> => {
  config.cancelToken?.throwIfRequested();
  if (config.signal?.aborted) throw canceledRequest(config);
  return new Promise<T>((resolve, reject) => {
    let settled = false;
    const cleanup = () => {
      config.signal?.removeEventListener?.('abort', abort);
      config.cancelToken?.unsubscribe(cancel);
    };
    const cancel = (error: unknown) => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(error);
    };
    const abort = () => cancel(canceledRequest(config));
    config.signal?.addEventListener?.('abort', abort);
    config.cancelToken?.subscribe(cancel);
    if (config.signal?.aborted) abort();
    if (settled) return;
    try {
      void read().then(value => {
        if (settled) return;
        settled = true;
        cleanup();
        resolve(value);
      }, cancel);
    } catch (error) {
      cancel(error);
    }
  });
};

export function setupAuthInterceptor(axiosInstance: AxiosInstance, provider: TokenProvider) {
  const assertSession = (config: AuthRequestConfig) => {
    if (config._syntrixAuthSessionVersion !== provider.getSessionVersion()) {
      if (config.signal?.aborted && abortReason(config) !== undefined) throw abortReason(config);
      throw new AuthSessionChangedError();
    }
  };

  axiosInstance.interceptors.request.use(async (config) => {
    const authConfig = config as AuthRequestConfig;
    assertSession(authConfig);
    let token: string | null;
    try {
      token = await waitForCredentials(authConfig, () => provider.getToken());
    } catch (error) {
      if (!config.signal?.aborted) assertSession(authConfig);
      throw error;
    }
    assertSession(authConfig);
    if (token) {
      config.headers.set('Authorization', `Bearer ${token}`);
    } else {
      config.headers.delete('Authorization');
    }
    return config;
  }, undefined, {
    runWhen: config => {
      // Axios evaluates admission synchronously before scheduling any asynchronous
      // interceptor. Preserve an explicitly bound scope and the version on retries.
      (config as AuthRequestConfig)._syntrixAuthSessionVersion ??= provider.getSessionVersion();
      return true;
    },
  });

  axiosInstance.interceptors.response.use(
    (response) => response,
    async (error: AxiosError) => {
      const config = error.config as AuthRequestConfig;

      if (isCancel(error) && config?.signal?.aborted) {
        // Axios adapters create their own cancellation objects. Retain their public
        // error shape and link the exact signal reason for the owner's drain.
        Object.assign(error, { cause: abortReason(config) });
        return Promise.reject(error);
      }

      if (!config || !error.response) {
        return Promise.reject(error);
      }

      const status = error.response.status;
      const data = error.response.data as any;

      if (status === 401 || status === 403) {
        assertSession(config);
      }

      // Handle rate limiting (429)
      if (status === 429) {
        const retryAfterHeader = error.response.headers['retry-after'];
        const retryAfter = retryAfterHeader ? parseInt(retryAfterHeader, 10) : 60;
        const syntrixError = SyntrixError.fromResponse(status, data, retryAfter);
        return Promise.reject(syntrixError);
      }

      // Handle auth errors with token refresh
      if ((status === 401 || status === 403) && !config._retry) {
        config._retry = true;
        try {
          const newToken = await waitForCredentials(config, () => provider.refreshToken());
          assertSession(config);
          config.headers.set('Authorization', `Bearer ${newToken}`);
          return axiosInstance(config);
        } catch (refreshError) {
          if (config.signal?.aborted) return Promise.reject(refreshError);
          assertSession(config);
          // Convert to SyntrixError for consistent error handling
          if (isCancel(refreshError) || refreshError instanceof AuthSessionChangedError || refreshError instanceof SyntrixError) {
            return Promise.reject(refreshError);
          }
          return Promise.reject(SyntrixError.fromResponse(401, { code: 'UNAUTHORIZED', message: 'Authentication failed' }));
        }
      }

      // Convert all API errors to SyntrixError
      if (data && (data.code || data.message || data.errors)) {
        return Promise.reject(SyntrixError.fromResponse(status, data));
      }

      return Promise.reject(error);
    }
  );
}
