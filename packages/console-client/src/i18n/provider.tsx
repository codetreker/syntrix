import { createContext, useContext, useEffect, useSyncExternalStore, type ReactNode } from 'react';
import { I18nProvider } from '@lingui/react';
import { applicationLanguageRuntime, type LanguageRuntime } from './runtime';

const LanguageContext = createContext<LanguageRuntime | null>(null);

export const LanguageProvider = ({ children, runtime = applicationLanguageRuntime }: {
  children: ReactNode;
  runtime?: LanguageRuntime;
}) => {
  runtime.initialize();
  useEffect(() => runtime.connect(), [runtime]);
  return (
    <LanguageContext.Provider value={runtime}>
      <I18nProvider i18n={runtime.i18n}>{children}</I18nProvider>
    </LanguageContext.Provider>
  );
};

export const useLanguage = () => {
  const runtime = useContext(LanguageContext);
  if (!runtime) throw new Error('useLanguage requires a LanguageProvider.');
  const state = useSyncExternalStore(runtime.subscribe, runtime.getSnapshot);
  return { ...state, setPreference: runtime.setPreference };
};
