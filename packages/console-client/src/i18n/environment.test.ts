import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  createBrowserLanguageEnvironment,
  languagePreferenceKey,
  LanguagePersistenceError,
} from './environment';

beforeEach(() => { window.localStorage.clear(); });
afterEach(() => { vi.restoreAllMocks(); });

describe('browser language environment', () => {
  it('uses only the Console language storage key and current browser languages', () => {
    const environment = createBrowserLanguageEnvironment();
    expect(environment.readPreference()).toBeNull();
    environment.writePreference('zh-CN');
    expect(window.localStorage.getItem(languagePreferenceKey)).toBe('zh-CN');
    expect(environment.readPreference()).toBe('zh-CN');
    expect(environment.getLanguages()).toEqual(navigator.languages);
    environment.setDocumentLanguage('zh-CN');
    expect(document.documentElement.lang).toBe('zh-CN');
  });

  it('observes relevant storage and language changes even when the tab is hidden', () => {
    const environment = createBrowserLanguageEnvironment();
    const preference = vi.fn();
    const languages = vi.fn();
    vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
    const disconnect = environment.subscribe(preference, languages);
    try {
      window.dispatchEvent(new StorageEvent('storage', { key: 'other', storageArea: window.localStorage }));
      window.dispatchEvent(new StorageEvent('storage', { key: languagePreferenceKey, storageArea: window.sessionStorage }));
      expect(preference).not.toHaveBeenCalled();
      window.dispatchEvent(new StorageEvent('storage', { key: languagePreferenceKey, storageArea: window.localStorage }));
      window.dispatchEvent(new StorageEvent('storage', { key: null, storageArea: window.localStorage }));
      window.dispatchEvent(new Event('languagechange'));
      expect(preference).toHaveBeenCalledTimes(2);
      expect(languages).toHaveBeenCalledOnce();
    } finally {
      disconnect();
    }
    window.dispatchEvent(new StorageEvent('storage', { key: languagePreferenceKey, storageArea: window.localStorage }));
    window.dispatchEvent(new Event('languagechange'));
    expect(preference).toHaveBeenCalledTimes(2);
    expect(languages).toHaveBeenCalledOnce();
  });

  it('delegates inaccessible storage to the preference error boundary', () => {
    const environment = createBrowserLanguageEnvironment();
    const preference = vi.fn();
    const disconnect = environment.subscribe(preference, vi.fn());
    const failure = new DOMException('Access denied', 'SecurityError');
    vi.spyOn(window, 'localStorage', 'get').mockImplementation(() => { throw failure; });
    try {
      window.dispatchEvent(new StorageEvent('storage', { key: 'unrelated' }));
      expect(preference).not.toHaveBeenCalled();
      window.dispatchEvent(new StorageEvent('storage', { key: languagePreferenceKey }));
      expect(preference).toHaveBeenCalledOnce();
      expect(() => environment.readPreference()).toThrow(failure);
      expect(() => environment.writePreference('en')).toThrow(failure);
    } finally {
      disconnect();
    }
  });

  it('reports structured failure categories without stored values', () => {
    const report = vi.spyOn(console, 'error').mockImplementation(() => {});
    const environment = createBrowserLanguageEnvironment();
    environment.reportError(new LanguagePersistenceError('write', new DOMException('Private message', 'SecurityError')));
    environment.reportError(new LanguagePersistenceError('read', undefined));
    expect(report.mock.calls).toEqual([
      [{ component: 'console-language', operation: 'write', category: 'persistence', causeType: 'SecurityError' }],
      [{ component: 'console-language', operation: 'read', category: 'persistence', causeType: 'undefined' }],
    ]);
  });
});
