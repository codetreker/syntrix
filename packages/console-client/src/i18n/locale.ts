export type SupportedLocale = 'en' | 'zh-CN';
export type LanguagePreference = 'system' | SupportedLocale;

export const isLanguagePreference = (value: unknown): value is LanguagePreference =>
  value === 'system' || value === 'en' || value === 'zh-CN';

export const resolveLocale = (
  preference: LanguagePreference,
  browserLanguages: readonly string[],
): SupportedLocale => {
  if (preference !== 'system') return preference;

  for (const language of browserLanguages) {
    let locale: Intl.Locale;
    try {
      locale = new Intl.Locale(language);
    } catch (error) {
      if (error instanceof RangeError) continue;
      throw error;
    }
    if (locale.language === 'en') return 'en';
    if (locale.language === 'zh' && locale.maximize().script === 'Hans') return 'zh-CN';
  }
  return 'en';
};
