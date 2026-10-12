import assert from 'node:assert/strict';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { getConfig } from '@lingui/conf';
import { createCompiledCatalog, getCatalogs } from '@lingui/cli/api';
import { compileMessageOrThrow } from '@lingui/message-utils/compileMessage';
import { formatter } from '@lingui/format-po';

const sorted = (values: Iterable<string>) => [...new Set(values)].sort();
const record = (value: unknown, description: string): Record<string, unknown> => {
  assert.ok(value !== null && typeof value === 'object' && !Array.isArray(value), description);
  return value as Record<string, unknown>;
};
const stringArray = (value: unknown, description: string): string[] => {
  assert.ok(Array.isArray(value) && value.every(item => typeof item === 'string'), description);
  return value as string[];
};

const messageContract = (message: string) => {
  const argumentsUsed = new Set<string>();
  const tagsUsed = new Set<string>();
  const visit = (tokens: unknown, stack: string[]) => {
    assert.ok(Array.isArray(tokens), 'Invalid compiled message');
    for (const token of tokens) {
      if (typeof token === 'string') {
        for (const match of token.matchAll(/<(\/?)(\d+|[a-zA-Z][\w.-]*)(\/?)>/g)) {
          const [, closing, tag, selfClosing] = match;
          tagsUsed.add(tag!);
          if (closing) assert.equal(stack.pop(), tag, `Unbalanced rich-text tag </${tag}>`);
          else if (!selfClosing) stack.push(tag!);
        }
        continue;
      }
      assert.ok(Array.isArray(token), 'Invalid ICU token');
      const [name, type, format] = token as unknown[];
      assert.ok(typeof name === 'string' || typeof name === 'number', 'Invalid ICU argument');
      assert.ok(type === undefined || ['plural', 'selectordinal', 'select', 'number', 'date', 'time'].includes(String(type)), 'Unsupported ICU formatter');
      argumentsUsed.add(`${name}:${type ?? 'argument'}`);
      if (type === 'plural' || type === 'selectordinal' || type === 'select') {
        const branches = record(format, `Invalid ${type} branches`);
        assert.ok(Object.hasOwn(branches, 'other'), `Missing other branch for ${name}`);
        for (const [branch, content] of Object.entries(branches)) {
          if (branch === 'offset') continue;
          const branchStack = [...stack];
          visit(content, branchStack);
          assert.deepEqual(branchStack, stack, `Unbalanced rich-text tags in ${name}:${branch}`);
        }
      }
    }
  };
  const stack: string[] = [];
  visit(compileMessageOrThrow(message), stack);
  assert.deepEqual(stack, [], 'Unclosed rich-text tags');
  return { arguments: sorted(argumentsUsed), tags: sorted(tagsUsed) };
};

export const validateTranslation = (source: string, translation: unknown) => {
  assert.equal(typeof translation, 'string', 'Missing translation');
  assert.ok((translation as string).trim(), 'Empty translation');
  assert.deepEqual(messageContract(translation as string), messageContract(source), 'Placeholder or rich-text tag mismatch');
};

const sourceMetadata = (entry: Record<string, unknown>) => {
  const origin = entry.origin === undefined ? [] : entry.origin;
  assert.ok(Array.isArray(origin), 'Invalid message origins');
  return {
    message: entry.message,
    context: entry.context,
    comments: sorted(entry.comments === undefined ? [] : stringArray(entry.comments, 'Invalid message comments')),
    origins: sorted(origin.map((item: unknown) => {
      assert.ok(Array.isArray(item) && typeof item[0] === 'string', 'Invalid message origin');
      return item[0] as string;
    })),
  };
};

export const validateCatalog = async (
  extractedValue: unknown,
  catalogValue: unknown,
  { locale, sourceLocale }: { locale: string; sourceLocale: string },
) => {
  const extracted = record(extractedValue, 'Source extraction failed');
  const catalog = record(catalogValue, `Missing ${locale} catalog`);
  assert.deepEqual(Object.keys(catalog).sort(), Object.keys(extracted).sort(), `${locale}: catalog entries differ from current source; run i18n:extract`);
  const po = formatter({ lineNumbers: false });
  const serialized = await po.serialize(
    extracted as Parameters<typeof po.serialize>[0],
    { locale: sourceLocale, sourceLocale, filename: 'messages.po', existing: undefined },
  );
  const source = record(await po.parse(serialized, {
    locale: sourceLocale, sourceLocale, filename: 'messages.po',
  }), 'Source serialization failed');
  for (const [id, rawEntry] of Object.entries(catalog)) {
    try {
      const entry = record(rawEntry, 'Invalid catalog entry');
      const original = record(source[id], 'Missing extracted message');
      assert.equal(typeof original.message, 'string', 'Missing source message');
      const message = original.message as string;
      assert.ok(!/\p{Script=Han}/u.test(message), 'Source messages must be written in English');
      assert.ok(!entry.obsolete, 'Obsolete message');
      assert.deepEqual(sourceMetadata(entry), sourceMetadata(original), 'Source metadata drift; run i18n:extract');
      const flags = entry.extra === undefined ? [] : record(entry.extra, 'Invalid catalog metadata').flags;
      assert.ok(flags === undefined || !stringArray(flags, 'Invalid catalog flags').includes('fuzzy'), 'Fuzzy translation');
      if (locale === sourceLocale) assert.equal(entry.translation, message, 'English translation differs from source message');
      validateTranslation(message, entry.translation);
    } catch (cause) {
      const detail = cause instanceof Error ? cause.message : String(cause);
      throw new Error(`${locale}:${id}: ${detail}`, { cause });
    }
  }
  const compiled = createCompiledCatalog(locale, Object.fromEntries(
    Object.entries(catalog).map(([id, rawEntry]) => [id, record(rawEntry, 'Invalid catalog entry').translation as string]),
  ), { strict: true, namespace: 'es' });
  assert.deepEqual(compiled.errors, [], `${locale}: catalog compilation failed`);
};

export const checkCatalogs = async (config: Awaited<ReturnType<typeof getConfig>>) => {
  assert.equal(config.sourceLocale, 'en', 'Console source locale must be English');
  assert.deepEqual(config.locales, ['en', 'zh-CN'], 'Console must provide English and Simplified Chinese');
  const catalogs = await getCatalogs(config);
  assert.equal(catalogs.length, 1, 'Configure one application catalog per locale');
  const catalog = catalogs[0]!;
  const extracted = await catalog.collect();
  assert.ok(extracted && Object.keys(extracted).length, 'No application messages extracted');
  for (const locale of config.locales) {
    await validateCatalog(extracted, await catalog.read(locale), { locale, sourceLocale: 'en' });
  }
  return Object.keys(extracted).length;
};

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const count = await checkCatalogs(await getConfig());
  console.log(`Validated ${count} current messages in English and Simplified Chinese.`);
}
