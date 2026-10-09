import { describe, expect, it } from 'bun:test';
import { TriggerHandler, WebhookPayload } from './trigger-handler';

describe('TriggerHandler', () => {
  it('throws when preIssuedToken is missing', () => {
    const payload: WebhookPayload = {
      triggerId: 't1',
      collection: 'users',
    };

    expect(() => new TriggerHandler(payload, 'http://localhost')).toThrow(
      'Missing preIssuedToken in webhook payload'
    );
  });

  it('initializes TriggerClient when token is present', () => {
    const payload: WebhookPayload = {
      triggerId: 't1',
      collection: 'users',
      preIssuedToken: 'token-123',
    };

    const handler = new TriggerHandler(payload, 'http://localhost');
    expect(handler.syntrix).toBeDefined();
  });

  it('accepts documentId on payload', () => {
    const payload: WebhookPayload = {
      triggerId: 't1',
      collection: 'users',
      documentId: 'doc-1',
      preIssuedToken: 'token-123',
    };

    const handler = new TriggerHandler(payload, 'http://localhost');
    expect(handler.syntrix).toBeDefined();
  });
});
