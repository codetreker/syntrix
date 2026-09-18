import { afterAll, expect, test } from 'bun:test';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { DefaultTokenProvider } from './provider.js';
import * as lifecycle from './lifecycle.js';

const copies: string[] = [];
afterAll(async () => { await Promise.all(copies.map(directory => rm(directory, { recursive: true, force: true }))); });
const loadIndependentCopy = async (): Promise<typeof lifecycle> => {
  const source = await Bun.file(new URL('./lifecycle.ts', import.meta.url)).text();
  const javascript = new Bun.Transpiler({ loader: 'ts' }).transformSync(source);
  const temporary = resolve(import.meta.dir, '../../../../../.tmp');
  await mkdir(temporary, { recursive: true });
  const directory = await mkdtemp(join(temporary, 'auth-ownership-'));
  copies.push(directory);
  const file = join(directory, 'lifecycle.mjs');
  await writeFile(file, javascript);
  return import(pathToFileURL(file).href);
};

test('independent bundled registry copies share provider ownership and drain before credential installation', async () => {
  const replicaCopy = await loadIndependentCopy();
  const otherCopy = await loadIndependentCopy();
  const provider = new DefaultTokenProvider({ token: 'A' });
  const otherProvider = new DefaultTokenProvider({ token: 'other' });
  expect(replicaCopy.registerAuthOwner).not.toBe(lifecycle.registerAuthOwner);
  expect(otherCopy.registerAuthOwner).not.toBe(replicaCopy.registerAuthOwner);
  expect(replicaCopy.supportsAuthOwnership(provider)).toBe(true);
  let invalidated = false;
  let release!: () => void;
  const closing = new Promise<void>(resolve => { release = resolve; });
  replicaCopy.registerAuthOwner(provider, {
    acceptsToken: (token, previousToken) => { expect(previousToken).toBe('A'); return token === previousToken; },
    invalidate: () => { invalidated = true; },
    close: () => closing,
  });
  otherCopy.enableAuthOwnership(provider);
  expect(lifecycle.authOwnersAcceptToken(provider, 'B', 'A')).toBe(false);
  expect(otherCopy.authOwnersAcceptToken(provider, 'B', 'A')).toBe(false);
  expect(otherCopy.authOwnersAcceptToken(otherProvider, 'B', 'other')).toBe(true);
  const descriptor = Object.getOwnPropertyDescriptor(provider, Symbol.for('@syntrix/client/auth-ownership/v1'))!;
  expect(descriptor.enumerable).toBe(false);
  expect(descriptor.writable).toBe(false);
  expect(descriptor.configurable).toBe(false);
  provider.setToken('B');
  expect(invalidated).toBe(true);
  let installed = false;
  const token = provider.getToken().then(value => { installed = true; return value; });
  await Promise.resolve(); expect(installed).toBe(false);
  release();
  expect(await token).toBe('B');
  expect(await otherProvider.getToken()).toBe('other');
});

test('owners are per instance and failures drain every owner before surfacing', async () => {
  const provider = new DefaultTokenProvider({ token: 'opaque' });
  expect(lifecycle.supportsAuthOwnership(Object.create(provider))).toBe(false);
  expect(lifecycle.supportsAuthOwnership({})).toBe(false);
  expect(() => lifecycle.registerAuthOwner({}, {} as never)).toThrow('lifecycle');
  const failure = new Error('invalidate failed');
  const order: string[] = [];
  lifecycle.registerAuthOwner(provider, {
    acceptsToken: () => true,
    invalidate: () => { order.push('invalidate 1'); throw failure; },
    close: async () => { order.push('close 1'); },
  });
  const unregister = lifecycle.registerAuthOwner(provider, {
    acceptsToken: () => false, invalidate: () => { throw new Error('unregistered'); }, close: async () => {},
  });
  unregister();
  lifecycle.registerAuthOwner(provider, {
    acceptsToken: () => true, invalidate: () => { order.push('invalidate 2'); },
    close: async () => { order.push('close 2'); throw new Error('close failed'); },
  });
  provider.setToken('B');
  await expect(provider.getToken()).rejects.toBe(failure);
  expect(order).toEqual(['invalidate 1', 'invalidate 2', 'close 1', 'close 2']);
});
