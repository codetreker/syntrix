import { TriggerClient } from './trigger-client';

export interface WebhookPayload {
  triggerId: string;
  database?: string;
  collection: string;
  documentId?: string;
  preIssuedToken?: string;
  before?: unknown;
  after?: unknown;
}

export class TriggerHandler {
  public readonly syntrix: TriggerClient;

  constructor(payload: WebhookPayload, baseUrl: string, database?: string) {
    if (!payload.preIssuedToken) {
      throw new Error('Missing preIssuedToken in webhook payload');
    }
    // Use database from parameter, then from payload, then default
    const db = database || payload.database || 'default';
    this.syntrix = new TriggerClient(baseUrl, payload.preIssuedToken, db);
  }
}
