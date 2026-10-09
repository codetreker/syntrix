import { describe, it, expect, mock } from 'bun:test';
import { RestTransport } from './rest-transport';

const TEST_DB = 'test-db';

describe('RestTransport buildPath', () => {
  it('should build document paths with database prefix', async () => {
    const axiosMock: any = { get: mock(async () => ({ data: { ok: true } })) };
    const transport = new RestTransport(axiosMock, TEST_DB);

    await transport.get('users/1');

    expect(axiosMock.get).toHaveBeenCalledWith('/api/v1/databases/test-db/documents/users/1');
  });

  it('should keep paths that already include /api/v1 unchanged', async () => {
    const axiosMock: any = { get: mock(async () => ({ data: { ok: true } })) };
    const transport = new RestTransport(axiosMock, TEST_DB);

    await transport.get('/api/v1/custom/endpoint');

    expect(axiosMock.get).toHaveBeenCalledWith('/api/v1/custom/endpoint');
  });
});
