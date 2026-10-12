import type { LanguagePreference } from './locale';

export const languagePreferenceKey = 'syntrix.console.languagePreference';

export class LanguagePersistenceError extends Error {
  constructor(readonly operation: 'read' | 'write', cause: unknown) {
    super(`Unable to ${operation} the Console language preference.`, { cause });
    this.name = 'LanguagePersistenceError';
  }
}

export interface LanguageEnvironment {
  readPreference: () => string | null;
  writePreference: (preference: LanguagePreference) => void;
  getLanguages: () => readonly string[];
  subscribe: (onPreferenceChange: () => void, onLanguagesChange: () => void) => () => void;
  setDocumentLanguage: (locale: string) => void;
  reportError: (error: LanguagePersistenceError) => void;
}

export const createBrowserLanguageEnvironment = (): LanguageEnvironment => ({
  readPreference: () => window.localStorage.getItem(languagePreferenceKey),
  writePreference: preference => window.localStorage.setItem(languagePreferenceKey, preference),
  getLanguages: () => navigator.languages,
  subscribe: (onPreferenceChange, onLanguagesChange) => {
    const onStorage = (event: StorageEvent) => {
      if (event.key !== null && event.key !== languagePreferenceKey) return;
      let storage: Storage;
      try {
        storage = window.localStorage;
      } catch {
        onPreferenceChange();
        return;
      }
      if (event.storageArea === storage) onPreferenceChange();
    };
    window.addEventListener('storage', onStorage);
    window.addEventListener('languagechange', onLanguagesChange);
    return () => {
      window.removeEventListener('storage', onStorage);
      window.removeEventListener('languagechange', onLanguagesChange);
    };
  },
  setDocumentLanguage: locale => { document.documentElement.lang = locale; },
  reportError: error => {
    console.error({
      component: 'console-language',
      operation: error.operation,
      category: 'persistence',
      causeType: error.cause instanceof Error || error.cause instanceof DOMException
        ? error.cause.name : typeof error.cause,
    });
  },
});
