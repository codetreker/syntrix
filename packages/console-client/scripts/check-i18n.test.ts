import { describe, expect, it } from 'vitest';
import { mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { makeConfig } from '@lingui/conf';
import { getCatalogs } from '@lingui/cli/api';
import { formatter } from '@lingui/format-po';
import { generateMessageId } from '@lingui/message-utils/generateMessageId';
import { checkCatalogs, validateCatalog, validateTranslation } from './check-i18n';

const greetingId = generateMessageId('Hello {name}');
const source = {
  [greetingId]: { message: 'Hello {name}', context: undefined, origin: [['src/Greeting.tsx', 1]], comments: [] },
};
const catalog = (translation: string) => ({
  [greetingId]: { ...source[greetingId], origin: [['src/Greeting.tsx']], translation },
});

describe('translation contract', () => {
  it('accepts translated prose while preserving interpolations', () => {
    expect(() => validateTranslation('Hello {name}', '你好，{name}')).not.toThrow();
  });

  it.each([undefined, '', ' \n '])('rejects missing or empty translations %j', translation => {
    expect(() => validateTranslation('Hello', translation)).toThrow(/Missing translation|Empty translation/);
  });

  it('rejects dropped or renamed interpolation placeholders', () => {
    expect(() => validateTranslation('Hello {name}', '你好')).toThrow('Placeholder or rich-text tag mismatch');
    expect(() => validateTranslation('Hello {name}', '你好，{person}')).toThrow('Placeholder or rich-text tag mismatch');
  });

  it('rejects changed ICU formatter contracts', () => {
    expect(() => validateTranslation('{count, number}', '{count}')).toThrow('Placeholder or rich-text tag mismatch');
  });

  it('accepts plural languages with different branch counts', () => {
    expect(() => validateTranslation(
      '{count, plural, one {One item} other {# items}}',
      '{count, plural, other {# 项}}',
    )).not.toThrow();
  });

  it('validates nested ICU arguments', () => {
    expect(() => validateTranslation(
      '{count, plural, one {Hello {name}} other {Hello {name}}}',
      '{count, plural, other {你好}}',
    )).toThrow('Placeholder or rich-text tag mismatch');
  });

  it('validates rich-text tag names and balanced nesting', () => {
    expect(() => validateTranslation('Read <0>documentation</0>', '阅读<0>文档</0>')).not.toThrow();
    expect(() => validateTranslation('Read <0>documentation</0>', '阅读文档')).toThrow('Placeholder or rich-text tag mismatch');
    expect(() => validateTranslation('<0>Open</0>', '<0>打开')).toThrow('Unclosed rich-text tags');
    expect(() => validateTranslation('<0>Open</0>', '<0>打开</1>')).toThrow('Unbalanced rich-text tag');
  });

  it('validates tags independently within ICU branches', () => {
    expect(() => validateTranslation(
      '{count, plural, one {<0>One</0>} other {<0>Many</0>}}',
      '{count, plural, other {<0>多项}}',
    )).toThrow('Unbalanced rich-text tags');
  });

  it('accepts matching self-closing tags', () => {
    expect(() => validateTranslation('Before <br/> after', '之前<br/>之后')).not.toThrow();
  });

  it('rejects invalid ICU messages', () => {
    expect(() => validateTranslation('Hello {name}', '你好 {name')).toThrow();
  });
});

describe('catalog gate', () => {
  it('accepts a complete source-matching Chinese catalog', async () => {
    await expect(validateCatalog(source, catalog('你好，{name}'), { locale: 'zh-CN', sourceLocale: 'en' })).resolves.toBeUndefined();
  });

  it('fails a Chinese catalog missing a current message', async () => {
    await expect(validateCatalog(source, {}, { locale: 'zh-CN', sourceLocale: 'en' })).rejects.toThrow('catalog entries differ from current source');
  });

  it('fails a Chinese translation dropping interpolation', async () => {
    await expect(validateCatalog(source, catalog('你好'), { locale: 'zh-CN', sourceLocale: 'en' })).rejects.toThrow('Placeholder or rich-text tag mismatch');
  });

  it('rejects blank Chinese translations', async () => {
    await expect(validateCatalog(source, catalog(''), { locale: 'zh-CN', sourceLocale: 'en' })).rejects.toThrow('Empty translation');
  });

  it('rejects stale or fuzzy entries', async () => {
    await expect(validateCatalog(source, {
      ...catalog('你好，{name}'), stale: { message: 'Old message', translation: '旧文案' },
    }, { locale: 'zh-CN', sourceLocale: 'en' })).rejects.toThrow('catalog entries differ from current source');
    await expect(validateCatalog(source, {
      [greetingId]: { ...catalog('你好，{name}')[greetingId], extra: { flags: ['fuzzy'] } },
    }, { locale: 'zh-CN', sourceLocale: 'en' })).rejects.toThrow('Fuzzy translation');
  });

  it('rejects obsolete entries and changed source ownership', async () => {
    await expect(validateCatalog(source, {
      [greetingId]: { ...catalog('你好，{name}')[greetingId], obsolete: true },
    }, { locale: 'zh-CN', sourceLocale: 'en' })).rejects.toThrow('Obsolete message');
    await expect(validateCatalog(source, {
      [greetingId]: { ...catalog('你好，{name}')[greetingId], origin: [['src/Other.tsx']] },
    }, { locale: 'zh-CN', sourceLocale: 'en' })).rejects.toThrow('Source metadata drift');
  });

  it('requires English catalog translations to equal source messages', async () => {
    await expect(validateCatalog(source, catalog('Hello {name}'), { locale: 'en', sourceLocale: 'en' })).resolves.toBeUndefined();
    await expect(validateCatalog(source, catalog('Welcome {name}'), { locale: 'en', sourceLocale: 'en' })).rejects.toThrow('English translation differs from source');
  });

  it('checks fresh extraction without mutating catalogs and rejects broken Chinese fixtures', async () => {
    const temporaryRoot = resolve(process.cwd(), '.tmp');
    await mkdir(temporaryRoot, { recursive: true });
    const directory = await mkdtemp(resolve(temporaryRoot, 'i18n-check-'));
    try {
      await mkdir(resolve(directory, 'src'));
      await writeFile(resolve(directory, 'src/Greeting.ts'), [
        "import { msg } from '@lingui/core/macro';",
        "const name = 'developer';",
        'export const greeting = msg`Hello ${name}`;',
      ].join('\n'));
      const config = makeConfig({
        rootDir: directory,
        sourceLocale: 'en',
        locales: ['en', 'zh-CN'],
        catalogs: [{ path: '<rootDir>/locales/{locale}/messages', include: ['<rootDir>/src'] }],
        format: formatter({ lineNumbers: false }),
      });
      const [fixtureCatalog] = await getCatalogs(config);
      const extracted = await fixtureCatalog!.collect();
      expect(extracted).toBeDefined();
      const withTranslation = (translation?: string) => Object.fromEntries(Object.entries(extracted!).map(([id, entry]) => [id, {
        ...entry,
        translation: translation ?? entry.message,
      }]));
      await fixtureCatalog!.write('en', withTranslation());
      await fixtureCatalog!.write('zh-CN', withTranslation('你好，{name}'));
      const before = await Promise.all(config.locales.map(locale => readFile(fixtureCatalog!.getFilename(locale), 'utf8')));
      await expect(checkCatalogs(config)).resolves.toBe(1);
      const after = await Promise.all(config.locales.map(locale => readFile(fixtureCatalog!.getFilename(locale), 'utf8')));
      expect(after).toEqual(before);

      await fixtureCatalog!.write('zh-CN', {});
      await expect(checkCatalogs(config)).rejects.toThrow('catalog entries differ from current source');
      await fixtureCatalog!.write('zh-CN', withTranslation('你好'));
      await expect(checkCatalogs(config)).rejects.toThrow('Placeholder or rich-text tag mismatch');
    } finally {
      await rm(directory, { recursive: true, force: true });
    }
  });
});
