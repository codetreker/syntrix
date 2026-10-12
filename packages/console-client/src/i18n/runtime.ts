import { setupI18n, type Messages } from '@lingui/core';
import { catalogs as defaultCatalogs } from './catalogs';
import {
  createBrowserLanguageEnvironment,
  LanguagePersistenceError,
  type LanguageEnvironment,
} from './environment';
import { isLanguagePreference, resolveLocale, type LanguagePreference, type SupportedLocale } from './locale';

export interface LanguageState {
  preference: LanguagePreference;
  effectiveLocale: SupportedLocale;
  persistenceError: LanguagePersistenceError | null;
}

export const createLanguageRuntime = ({
  environment = createBrowserLanguageEnvironment(),
  catalogs = defaultCatalogs,
}: {
  environment?: LanguageEnvironment;
  catalogs?: Record<SupportedLocale, Messages>;
} = {}) => {
  const i18n = setupI18n({ messages: catalogs });
  let state: LanguageState | undefined;
  const listeners = new Set<() => void>();

  const readPreference = () => {
    try {
      const value = environment.readPreference();
      if (value === null) return { preference: 'system' as const, error: null };
      if (!isLanguagePreference(value)) throw new Error('Unsupported saved language preference.');
      return { preference: value, error: null };
    } catch (cause) {
      const error = new LanguagePersistenceError('read', cause);
      environment.reportError(error);
      return { preference: null, error };
    }
  };

  const initialize = (): LanguageState => {
    if (state) return state;
    const saved = readPreference();
    const preference = saved.preference ?? 'system';
    const effectiveLocale = resolveLocale(preference, environment.getLanguages());
    state = { preference, effectiveLocale, persistenceError: saved.error };
    environment.setDocumentLanguage(effectiveLocale);
    i18n.activate(effectiveLocale);
    return state;
  };

  const update = (preference: LanguagePreference, persistenceError: LanguagePersistenceError | null) => {
    const current = initialize();
    const effectiveLocale = resolveLocale(preference, environment.getLanguages());
    if (current.preference === preference && current.effectiveLocale === effectiveLocale
      && current.persistenceError === persistenceError) return;
    state = { preference, effectiveLocale, persistenceError };
    environment.setDocumentLanguage(effectiveLocale);
    if (i18n.locale !== effectiveLocale) i18n.activate(effectiveLocale);
    listeners.forEach(listener => listener());
  };

  const refreshPreference = () => {
    const saved = readPreference();
    update(saved.preference ?? initialize().preference, saved.error);
  };

  let connections = 0;
  let disconnect: (() => void) | undefined;
  return {
    i18n,
    initialize,
    getSnapshot: initialize,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    setPreference: (preference: LanguagePreference) => {
      if (!isLanguagePreference(preference)) throw new Error('Unsupported language preference.');
      initialize();
      let persistenceError: LanguagePersistenceError | null = null;
      try {
        environment.writePreference(preference);
      } catch (cause) {
        persistenceError = new LanguagePersistenceError('write', cause);
        environment.reportError(persistenceError);
      }
      update(preference, persistenceError);
    },
    connect: () => {
      const current = initialize();
      if (connections++ === 0) {
        disconnect = environment.subscribe(refreshPreference, () => {
          const current = initialize();
          if (current.preference === 'system') update('system', current.persistenceError);
        });
        // Catch browser changes before the provider subscribes without replacing an unsaved selection.
        if (current.persistenceError === null) refreshPreference();
        else if (current.preference === 'system') update('system', current.persistenceError);
      }
      let connected = true;
      return () => {
        if (!connected) return;
        connected = false;
        if (--connections === 0) {
          disconnect?.();
          disconnect = undefined;
        }
      };
    },
  };
};

export type LanguageRuntime = ReturnType<typeof createLanguageRuntime>;
export const applicationLanguageRuntime = createLanguageRuntime();
export const i18n = applicationLanguageRuntime.i18n;
export const initializeLanguage = () => applicationLanguageRuntime.initialize();
