import { describe, expect, it, vi } from 'vitest';
import { createLanguageRuntime } from './runtime';
import type { LanguageEnvironment } from './environment';
import type { LanguagePreference } from './locale';

const fixture = (saved: string | null = null) => {
  let languages = ['en'];
  const handlers = new Set<{ preference: () => void; languages: () => void }>();
  const environment: LanguageEnvironment = {
    readPreference: vi.fn(() => saved),
    writePreference: vi.fn(value => { saved = value; }),
    getLanguages: () => languages,
    subscribe: vi.fn((preference, languages) => {
      const handler = { preference, languages };
      handlers.add(handler);
      return () => { handlers.delete(handler); };
    }),
    setDocumentLanguage: vi.fn(),
    reportError: vi.fn(),
  };
  const runtime = createLanguageRuntime({
    environment,
    catalogs: { en: { greeting: 'Hello' }, 'zh-CN': { greeting: '你好' } },
  });
  return {
    runtime, environment, handlers,
    externalPreference: (value: string | null) => {
      saved = value;
      handlers.forEach(handler => handler.preference());
    },
    browserLanguages: (value: string[]) => {
      languages = value;
      handlers.forEach(handler => handler.languages());
    },
  };
};

describe('language runtime', () => {
  it('activates saved language and document before any subscriber renders', () => {
    const { runtime, environment } = fixture('zh-CN');
    expect(environment.readPreference).not.toHaveBeenCalled();
    const state = runtime.initialize();
    expect(state).toEqual({ preference: 'zh-CN', effectiveLocale: 'zh-CN', persistenceError: null });
    expect(runtime.i18n._('greeting')).toBe('你好');
    expect(environment.setDocumentLanguage).toHaveBeenCalledWith('zh-CN');
    expect(runtime.initialize()).toBe(state);
    expect(environment.readPreference).toHaveBeenCalledOnce();
  });

  it('keeps application instances independent and notifies after activation', () => {
    const first = fixture();
    const second = fixture();
    const observed: string[] = [];
    const unsubscribe = first.runtime.subscribe(() => observed.push(first.runtime.i18n._('greeting')));
    second.runtime.initialize();
    first.runtime.setPreference('zh-CN');
    expect(observed).toEqual(['你好']);
    expect(second.runtime.i18n.locale).toBe('en');
    unsubscribe();
    first.runtime.setPreference('en');
    expect(observed).toEqual(['你好']);
  });

  it('tracks system language while preserving explicit selections', () => {
    const f = fixture();
    const disconnect = f.runtime.connect();
    f.browserLanguages(['zh-CN']);
    expect(f.runtime.getSnapshot()).toMatchObject({ preference: 'system', effectiveLocale: 'zh-CN' });
    f.runtime.setPreference('en');
    f.browserLanguages(['zh-CN']);
    expect(f.runtime.getSnapshot().effectiveLocale).toBe('en');
    f.runtime.setPreference('system');
    expect(f.runtime.getSnapshot().effectiveLocale).toBe('zh-CN');
    expect(f.environment.writePreference).toHaveBeenLastCalledWith('system');
    disconnect();
  });

  it('consumes cross-tab changes and removal without writing them back', () => {
    const f = fixture();
    const disconnect = f.runtime.connect();
    f.externalPreference('zh-CN');
    expect(f.runtime.i18n.locale).toBe('zh-CN');
    f.externalPreference(null);
    expect(f.runtime.getSnapshot().preference).toBe('system');
    expect(f.runtime.i18n.locale).toBe('en');
    expect(f.environment.writePreference).not.toHaveBeenCalled();
    disconnect();
  });

  it('reports invalid initial preference and resolves system language', () => {
    const { runtime, environment } = fixture('unsupported');
    const state = runtime.initialize();
    expect(state.preference).toBe('system');
    expect(state.persistenceError).toMatchObject({ operation: 'read' });
    expect(state.persistenceError?.cause).toEqual(new Error('Unsupported saved language preference.'));
    expect(environment.reportError).toHaveBeenCalledOnce();
    runtime.setPreference('zh-CN');
    expect(runtime.getSnapshot().persistenceError).toBeNull();
  });

  it('reports inaccessible initial storage with its original cause', () => {
    const f = fixture();
    const cause = new DOMException('Storage denied', 'SecurityError');
    vi.mocked(f.environment.readPreference).mockImplementation(() => { throw cause; });
    const state = f.runtime.initialize();
    expect(state.preference).toBe('system');
    expect(state.persistenceError?.cause).toBe(cause);
    const disconnect = f.runtime.connect();
    f.browserLanguages(['zh-CN']);
    expect(f.runtime.getSnapshot().effectiveLocale).toBe('zh-CN');
    disconnect();
  });

  it('keeps the current choice when an external preference cannot be read', () => {
    const f = fixture('zh-CN');
    const disconnect = f.runtime.connect();
    const failure = new DOMException('Storage denied', 'SecurityError');
    vi.mocked(f.environment.readPreference).mockImplementation(() => { throw failure; });
    f.externalPreference('en');
    expect(f.runtime.getSnapshot()).toMatchObject({ preference: 'zh-CN', effectiveLocale: 'zh-CN' });
    expect(f.runtime.getSnapshot().persistenceError?.cause).toBe(failure);
    disconnect();
  });

  it('applies a choice after save failure, keeps its cause, and allows recovery', () => {
    const f = fixture();
    const cause = new DOMException('Storage denied', 'SecurityError');
    vi.mocked(f.environment.writePreference).mockImplementation(() => { throw cause; });
    f.runtime.setPreference('zh-CN');
    expect(f.runtime.i18n.locale).toBe('zh-CN');
    expect(f.runtime.getSnapshot().persistenceError).toMatchObject({ operation: 'write', cause });
    expect(f.environment.reportError).toHaveBeenCalledWith(f.runtime.getSnapshot().persistenceError);
    vi.mocked(f.environment.writePreference).mockImplementation(() => {});
    f.runtime.setPreference('en');
    expect(f.runtime.getSnapshot().persistenceError).toBeNull();
  });

  it('does not persist invalid application preferences', () => {
    const f = fixture();
    expect(() => f.runtime.setPreference('fr' as LanguagePreference)).toThrow('Unsupported language preference.');
    expect(f.environment.writePreference).not.toHaveBeenCalled();
  });

  it('shares one connection and supports repeated clean setup', () => {
    const f = fixture();
    const stopFirst = f.runtime.connect();
    const stopSecond = f.runtime.connect();
    expect(f.handlers.size).toBe(1);
    stopFirst();
    stopFirst();
    expect(f.handlers.size).toBe(1);
    stopSecond();
    expect(f.handlers.size).toBe(0);
    const stopThird = f.runtime.connect();
    expect(f.handlers.size).toBe(1);
    stopThird();
    expect(f.handlers.size).toBe(0);
  });

  it('catches preference changes between initialization and subscription', () => {
    const f = fixture();
    f.runtime.initialize();
    f.externalPreference('zh-CN');
    expect(f.runtime.i18n.locale).toBe('en');
    const disconnect = f.runtime.connect();
    expect(f.runtime.i18n.locale).toBe('zh-CN');
    expect(f.environment.writePreference).not.toHaveBeenCalled();
    disconnect();
  });

  it('keeps an unsaved current-tab selection when reconnecting', () => {
    const f = fixture();
    vi.mocked(f.environment.writePreference).mockImplementation(() => { throw new Error('Denied'); });
    f.runtime.setPreference('zh-CN');
    const disconnect = f.runtime.connect();
    expect(f.runtime.i18n.locale).toBe('zh-CN');
    expect(f.runtime.getSnapshot().persistenceError?.operation).toBe('write');
    disconnect();
  });

  it('does not notify for an unchanged preference or language', () => {
    const f = fixture('en');
    const disconnect = f.runtime.connect();
    const listener = vi.fn();
    const unsubscribe = f.runtime.subscribe(listener);
    f.runtime.setPreference('en');
    f.externalPreference('en');
    expect(listener).not.toHaveBeenCalled();
    unsubscribe();
    disconnect();
  });
});
