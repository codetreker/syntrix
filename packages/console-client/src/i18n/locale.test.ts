import { describe, expect, it, vi } from 'vitest';
import { isLanguagePreference, resolveLocale } from './locale';

describe('locale resolution', () => {
  it.each([
    ['en', ['zh-CN'], 'en'],
    ['zh-CN', ['en'], 'zh-CN'],
    ['system', ['fr', 'zh-SG', 'en'], 'zh-CN'],
    ['system', ['en-GB', 'zh-CN'], 'en'],
    ['system', ['zh-Hant', 'en', 'zh-Hans'], 'en'],
    ['system', ['zh-TW', 'zh-HK', 'zh-MO', 'zh-Hans'], 'zh-CN'],
    ['system', ['zh-Hant-CN'], 'en'],
    ['system', ['zh-Hans-TW'], 'zh-CN'],
    ['system', ['zh'], 'zh-CN'],
    ['system', ['not_a_locale', 'zh-CN'], 'zh-CN'],
    ['system', ['fr'], 'en'],
    ['system', [], 'en'],
  ] as const)('resolves %s with %j to %s', (preference, languages, expected) => {
    expect(resolveLocale(preference, languages)).toBe(expected);
  });

  it('accepts only supported persisted preferences', () => {
    expect(['en', 'zh-CN', 'system'].every(isLanguagePreference)).toBe(true);
    expect(['', 'EN', 'zh', 'fr', null, 1].some(isLanguagePreference)).toBe(false);
  });

  it('does not hide unexpected locale implementation failures', () => {
    const failure = new TypeError('Locale implementation failed');
    const locale = vi.spyOn(Intl, 'Locale').mockImplementation(function () { throw failure; });
    try {
      expect(() => resolveLocale('system', ['en'])).toThrow(failure);
    } finally {
      locale.mockRestore();
    }
  });
});
