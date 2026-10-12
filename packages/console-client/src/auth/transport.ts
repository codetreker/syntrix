export type ConsoleSession = {
  developer: { id: string; username: string };
  expires_at: string;
  csrf_token: string;
};

export type AuthPhase = 'me' | 'login' | 'logout';
export type AuthErrorCategory =
  | 'unauthorized'
  | 'forbidden'
  | 'unavailable'
  | 'network'
  | 'timeout'
  | 'canceled'
  | 'invalid-response'
  | 'invalid-request'
  | 'not-found'
  | 'conflict'
  | 'busy';

export class AuthError extends Error {
  readonly category: AuthErrorCategory;
  readonly phase: AuthPhase;
  readonly status: number | undefined;
  readonly requestId: string | undefined;

  constructor(
    category: AuthErrorCategory,
    phase: AuthPhase,
    details: { status?: number; requestId?: string; cause?: unknown } = {},
  ) {
    super(`Console authentication ${phase} failed: ${category}`, { cause: details.cause });
    this.name = 'AuthError';
    this.category = category;
    this.phase = phase;
    this.status = details.status;
    this.requestId = details.requestId;
  }
}

export type AuthTransport = {
  me: (signal: AbortSignal) => Promise<ConsoleSession>;
  login: (username: string, password: string, signal: AbortSignal) => Promise<ConsoleSession>;
  logout: (csrfToken: string, signal: AbortSignal) => Promise<void>;
};

const categoryForStatus = (status: number): AuthErrorCategory => {
  switch (status) {
    case 401:
      return 'unauthorized';
    case 403:
      return 'forbidden';
    case 400:
    case 413:
    case 415:
      return 'invalid-request';
    case 404:
      return 'not-found';
    case 409:
      return 'conflict';
    default:
      return 'unavailable';
  }
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const readSession = (value: unknown, phase: AuthPhase, requestId?: string): ConsoleSession => {
  if (
    !isRecord(value) ||
    !isRecord(value.developer) ||
    typeof value.developer.id !== 'string' ||
    value.developer.id.length === 0 ||
    typeof value.developer.username !== 'string' ||
    value.developer.username.length === 0 ||
    typeof value.expires_at !== 'string' ||
    !Number.isFinite(Date.parse(value.expires_at)) ||
    typeof value.csrf_token !== 'string' ||
    value.csrf_token.length === 0
  ) {
    throw new AuthError('invalid-response', phase, { status: 200, requestId });
  }
  return {
    developer: { id: value.developer.id, username: value.developer.username },
    expires_at: value.expires_at,
    csrf_token: value.csrf_token,
  };
};

export const createAuthTransport = (
  options: { fetch?: typeof fetch; timeoutMs?: number } = {},
): AuthTransport => {
  const requestFetch = options.fetch ?? globalThis.fetch.bind(globalThis);
  const timeoutMs = options.timeoutMs ?? 30_000;
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    throw new RangeError('Authentication request timeout must be positive');
  }

  const request = async (
    phase: AuthPhase,
    signal: AbortSignal,
    init: RequestInit,
  ): Promise<ConsoleSession | undefined> => {
    if (signal.aborted) throw new AuthError('canceled', phase, { cause: signal.reason });
    const controller = new AbortController();
    let timedOut = false;
    const cancel = () => controller.abort(signal.reason);
    signal.addEventListener('abort', cancel, { once: true });
    const timeout = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, timeoutMs);
    let requestId: string | undefined;
    try {
      const response = await requestFetch(`/auth/v1/${phase}`, {
        ...init,
        credentials: 'same-origin',
        signal: controller.signal,
        headers: { Accept: 'application/json', ...init.headers },
      });
      const header = response.headers.get('X-Request-ID');
      if (header !== null && /^[a-f0-9]{32}$/.test(header)) requestId = header;
      const expectedStatus = phase === 'logout' ? 204 : 200;
      if (response.status !== expectedStatus) {
        throw new AuthError(response.ok ? 'invalid-response' : categoryForStatus(response.status), phase, {
          status: response.status,
          requestId,
        });
      }
      if (phase === 'logout') return;
      let body: unknown;
      try {
        body = await response.json();
      } catch (cause) {
        if (controller.signal.aborted) throw cause;
        throw new AuthError('invalid-response', phase, { status: response.status, requestId, cause });
      }
      if (controller.signal.aborted) {
        throw new AuthError(timedOut ? 'timeout' : 'canceled', phase, { requestId });
      }
      return readSession(body, phase, requestId);
    } catch (cause) {
      const error = cause instanceof AuthError ? cause : new AuthError(timedOut ? 'timeout' : signal.aborted ? 'canceled' : 'network', phase, {
        requestId,
        cause,
      });
      if (!['unauthorized', 'forbidden', 'canceled'].includes(error.category)) {
        console.error('Console authentication request failed', {
          component: 'auth-transport',
          phase,
          method: init.method,
          path: `/auth/v1/${phase}`,
          error_category: error.category,
          http_status: error.status,
          request_id: error.requestId,
          cause_type: error.cause instanceof Error ? error.cause.constructor.name : typeof error.cause,
        });
      }
      throw error;
    } finally {
      clearTimeout(timeout);
      signal.removeEventListener('abort', cancel);
    }
  };

  return {
    me: async (signal) => (await request('me', signal, { method: 'GET' })) as ConsoleSession,
    login: async (username, password, signal) =>
      (await request('login', signal, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, password }),
      })) as ConsoleSession,
    logout: async (csrfToken, signal) => {
      await request('logout', signal, { method: 'POST', headers: { 'X-CSRF-Token': csrfToken } });
    },
  };
};
