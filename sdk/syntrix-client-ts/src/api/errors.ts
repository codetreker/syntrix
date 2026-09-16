/**
 * Validation error from server-side validation
 */
export interface ValidationError {
  field: string;
  message: string;
}

export class AuthSessionChangedError extends Error {
  readonly code = 'AUTH_SESSION_CHANGED';

  constructor() {
    super('The authentication session changed while the operation was pending');
    this.name = 'AuthSessionChangedError';
  }
}

/**
 * API error response structure
 */
export interface ApiErrorResponse {
  code: string;
  message: string;
  errors?: ValidationError[];
}

/**
 * Custom error class for Syntrix API errors
 */
export class SyntrixError extends Error {
  public readonly retryAfter?: number;

  constructor(
    public readonly code: string,
    message: string,
    public readonly status: number,
    public readonly validationErrors?: ValidationError[],
    retryAfter?: number
  ) {
    super(message);
    this.name = 'SyntrixError';
    this.retryAfter = retryAfter;

    // Maintains proper stack trace for where error was thrown (only in V8)
    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, SyntrixError);
    }
  }

  /**
   * Create a SyntrixError from an API response
   */
  static fromResponse(status: number, data: any, retryAfter?: number): SyntrixError {
    const code = data?.code || 'UNKNOWN_ERROR';
    const message = data?.message || 'An error occurred';
    const validationErrors = data?.errors;

    return new SyntrixError(code, message, status, validationErrors, retryAfter);
  }

  /**
   * Check if this is a validation error
   */
  isValidationError(): boolean {
    return this.code === ErrorCodes.BAD_REQUEST && !!this.validationErrors?.length;
  }

  /**
   * Check if this is a rate limit error
   */
  isRateLimitError(): boolean {
    return this.code === ErrorCodes.RATE_LIMITED;
  }

  /**
   * Check if this is an authentication error
   */
  isAuthError(): boolean {
    return this.code === ErrorCodes.UNAUTHORIZED;
  }

  /**
   * Check if this is a permission error
   */
  isPermissionError(): boolean {
    return this.code === ErrorCodes.FORBIDDEN;
  }

  /**
   * Check if this is a not found error
   */
  isNotFoundError(): boolean {
    return this.code === ErrorCodes.NOT_FOUND || this.code === ErrorCodes.DATABASE_NOT_FOUND;
  }
}

/**
 * Error codes matching server implementation
 */
export const ErrorCodes = {
  // General errors
  BAD_REQUEST: 'BAD_REQUEST',
  UNAUTHORIZED: 'UNAUTHORIZED',
  FORBIDDEN: 'FORBIDDEN',
  NOT_FOUND: 'NOT_FOUND',
  CONFLICT: 'CONFLICT',
  PRECONDITION_FAILED: 'PRECONDITION_FAILED',
  REQUEST_TOO_LARGE: 'REQUEST_TOO_LARGE',
  RATE_LIMITED: 'RATE_LIMITED',
  INTERNAL_ERROR: 'INTERNAL_ERROR',

  // Replication errors
  RESYNC_REQUIRED: 'RESYNC_REQUIRED',
  REPLICATION_UNSUPPORTED: 'REPLICATION_UNSUPPORTED',
  REPLICATION_UNAVAILABLE: 'REPLICATION_UNAVAILABLE',
  REPLICATION_BUDGET_EXCEEDED: 'REPLICATION_BUDGET_EXCEEDED',
  DEADLINE_EXCEEDED: 'DEADLINE_EXCEEDED',

  // Database errors
  DATABASE_NOT_FOUND: 'DATABASE_NOT_FOUND',
  DATABASE_EXISTS: 'DATABASE_EXISTS',
  DATABASE_SUSPENDED: 'DATABASE_SUSPENDED',
  DATABASE_DELETING: 'DATABASE_DELETING',
  PROTECTED_DATABASE: 'PROTECTED_DATABASE',
  QUOTA_EXCEEDED: 'QUOTA_EXCEEDED',
  INVALID_SLUG: 'INVALID_SLUG',
  RESERVED_SLUG: 'RESERVED_SLUG',
  SLUG_IMMUTABLE: 'SLUG_IMMUTABLE',
  SLUG_EXISTS: 'SLUG_EXISTS',
  DISPLAY_NAME_REQUIRED: 'DISPLAY_NAME_REQUIRED',
} as const;

export type ErrorCode = typeof ErrorCodes[keyof typeof ErrorCodes];
