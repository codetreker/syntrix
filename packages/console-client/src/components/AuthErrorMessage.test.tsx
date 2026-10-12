import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { AuthError, type AuthErrorCategory, type AuthPhase } from '../auth';
import { LanguageProvider } from '../i18n/provider';
import { createLanguageRuntime } from '../i18n/runtime';
import { AuthErrorMessage } from './AuthErrorMessage';

describe('localized authentication errors', () => {
  it.each<[AuthErrorCategory, AuthPhase, string]>([
    ['unauthorized', 'login', 'The username or password is incorrect.'],
    ['unauthorized', 'me', 'Your session has ended.'],
    ['forbidden', 'login', 'This request was denied.'],
    ['unavailable', 'me', 'The Console service is unavailable.'],
    ['network', 'login', 'Could not reach the Console service.'],
    ['timeout', 'me', 'took too long to respond'],
    ['canceled', 'login', 'The request was interrupted.'],
    ['invalid-response', 'login', 'returned an unexpected response'],
    ['invalid-request', 'login', 'The request could not be accepted.'],
    ['not-found', 'me', 'could not find this request'],
    ['conflict', 'login', 'Your session changed.'],
    ['busy', 'login', 'Another account action is in progress.'],
  ])('distinguishes %s in %s without exposing causal diagnostics', (category, phase, copy) => {
    const runtime = createLanguageRuntime({ environment: {
      readPreference: () => 'en', writePreference: () => {}, getLanguages: () => ['en'],
      setDocumentLanguage: () => {}, subscribe: () => () => {}, reportError: vi.fn(),
    } });
    const error = new AuthError(category, phase, { cause: new Error('sensitive backend body'), requestId: 'support-123' });
    render(<LanguageProvider runtime={runtime}><div role="alert"><AuthErrorMessage error={error} /></div></LanguageProvider>);
    expect(screen.getByRole('alert')).toHaveTextContent(copy);
    expect(screen.getByRole('alert')).toHaveTextContent('Request ID: support-123');
    expect(screen.getByRole('alert')).not.toHaveTextContent('sensitive backend body');
  });
});
