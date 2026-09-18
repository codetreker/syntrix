import { expect, test } from 'bun:test';
import { createNamespace, parseLocalSubject } from './identity.js';
const jwt = (claims: object) => `${btoa('{}')}.${btoa(JSON.stringify(claims))}.sig`.replace(/=/g, '');
test('offline subject parsing validates identity without requiring an unexpired credential', () => {
  expect(parseLocalSubject(jwt({ sub: 'Alice', oid: 'Alice', exp: 0 }))).toBe('Alice');
  for (const token of [null, 'opaque', 'a.b.c', jwt({ sub: '' }), jwt({ sub: 'a', oid: 'b' }), jwt({ sub: 'a', oid: null })]) {
    expect(() => parseLocalSubject(token)).toThrow();
  }
});
test('namespace preserves scope and normalizes endpoint suffix only', async () => {
  const base = await createNamespace('https://example.com/prefix/', 'Db', 'local', 'users', 'Alice');
  expect(await createNamespace('https://example.com/prefix', 'Db', 'local', 'users', 'Alice')).toEqual(base);
  expect(base.hash).toMatch(/^[a-f0-9]{64}$/);
  for (const scope of [
    ['https://example.com/other', 'Db', 'local', 'users', 'Alice'],
    ['https://example.com/prefix', 'db', 'local', 'users', 'Alice'],
    ['https://example.com/prefix', 'Db', 'local', 'users', 'alice'],
  ]) expect((await createNamespace(...scope as [string,string,string,string,string])).hash).not.toBe(base.hash);
  for (const endpoint of ['https://user:pw@example.com', 'https://example.com/?a=1', 'https://example.com/#x', 'ftp://example.com']) {
    await expect(createNamespace(endpoint, 'db', 'n', 'a', 's')).rejects.toThrow();
  }
});
