import { describe, it, expect } from 'bun:test';
import { SyntrixError, ErrorCodes, ValidationError } from './errors';

describe('SyntrixError', () => {
  it('should create error with all properties', () => {
    const validationErrors: ValidationError[] = [
      { field: 'username', message: 'This field is required' }
    ];
    const error = new SyntrixError('BAD_REQUEST', 'Validation failed', 400, validationErrors);

    expect(error.code).toBe('BAD_REQUEST');
    expect(error.message).toBe('Validation failed');
    expect(error.status).toBe(400);
    expect(error.validationErrors).toEqual(validationErrors);
    expect(error.name).toBe('SyntrixError');
  });

  it('should create error from response', () => {
    const responseData = {
      code: 'NOT_FOUND',
      message: 'Document not found',
    };
    const error = SyntrixError.fromResponse(404, responseData);

    expect(error.code).toBe('NOT_FOUND');
    expect(error.message).toBe('Document not found');
    expect(error.status).toBe(404);
  });

  it('should create error from response with validation errors', () => {
    const responseData = {
      code: 'BAD_REQUEST',
      message: 'Validation failed',
      errors: [
        { field: 'password', message: 'Must be at least 8 characters' }
      ]
    };
    const error = SyntrixError.fromResponse(400, responseData);

    expect(error.isValidationError()).toBe(true);
    expect(error.validationErrors?.length).toBe(1);
    expect(error.validationErrors?.[0].field).toBe('password');
  });

  it('should create error from response with retry-after', () => {
    const responseData = {
      code: 'RATE_LIMITED',
      message: 'Too many requests',
    };
    const error = SyntrixError.fromResponse(429, responseData, 60);

    expect(error.isRateLimitError()).toBe(true);
    expect(error.retryAfter).toBe(60);
  });

  it('should handle null/undefined response data', () => {
    const error = SyntrixError.fromResponse(500, null);

    expect(error.code).toBe('UNKNOWN_ERROR');
    expect(error.message).toBe('An error occurred');
    expect(error.status).toBe(500);
  });

  describe('type checking methods', () => {
    it('isAuthError returns true for UNAUTHORIZED', () => {
      const error = new SyntrixError('UNAUTHORIZED', 'Invalid credentials', 401);
      expect(error.isAuthError()).toBe(true);
      expect(error.isPermissionError()).toBe(false);
    });

    it('isPermissionError returns true for FORBIDDEN', () => {
      const error = new SyntrixError('FORBIDDEN', 'Access denied', 403);
      expect(error.isPermissionError()).toBe(true);
      expect(error.isAuthError()).toBe(false);
    });

    it('isNotFoundError returns true for NOT_FOUND and DATABASE_NOT_FOUND', () => {
      const error1 = new SyntrixError('NOT_FOUND', 'Not found', 404);
      const error2 = new SyntrixError('DATABASE_NOT_FOUND', 'Database not found', 404);

      expect(error1.isNotFoundError()).toBe(true);
      expect(error2.isNotFoundError()).toBe(true);
    });

    it('isRateLimitError returns true for RATE_LIMITED', () => {
      const error = new SyntrixError('RATE_LIMITED', 'Too many requests', 429, undefined, 30);
      expect(error.isRateLimitError()).toBe(true);
    });
  });
});

describe('ErrorCodes', () => {
  it('should have all expected error codes', () => {
    expect(ErrorCodes.BAD_REQUEST).toBe('BAD_REQUEST');
    expect(ErrorCodes.UNAUTHORIZED).toBe('UNAUTHORIZED');
    expect(ErrorCodes.FORBIDDEN).toBe('FORBIDDEN');
    expect(ErrorCodes.NOT_FOUND).toBe('NOT_FOUND');
    expect(ErrorCodes.CONFLICT).toBe('CONFLICT');
    expect(ErrorCodes.PRECONDITION_FAILED).toBe('PRECONDITION_FAILED');
    expect(ErrorCodes.RATE_LIMITED).toBe('RATE_LIMITED');
    expect(ErrorCodes.INTERNAL_ERROR).toBe('INTERNAL_ERROR');
    expect(ErrorCodes.DATABASE_NOT_FOUND).toBe('DATABASE_NOT_FOUND');
    expect(ErrorCodes.DATABASE_SUSPENDED).toBe('DATABASE_SUSPENDED');
  });
});
