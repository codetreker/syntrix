import { act, StrictMode, useState } from 'react';
import { createRoot, type Root } from 'react-dom/client';
import { renderToString } from 'react-dom/server';
import { useLingui } from '@lingui/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { LanguageProvider, useLanguage } from './provider';
import { applicationLanguageRuntime, createLanguageRuntime, i18n, initializeLanguage } from './runtime';
import type { LanguageEnvironment } from './environment';

let container: HTMLDivElement;
let root: Root;
beforeEach(() => {
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

const fixture = () => {
  let connections = 0;
  let preference = 'en';
  const environment: LanguageEnvironment = {
    readPreference: () => preference,
    writePreference: value => { preference = value; },
    getLanguages: () => ['en'],
    subscribe: () => {
      connections++;
      return () => { connections--; };
    },
    setDocumentLanguage: locale => { document.documentElement.lang = locale; },
    reportError: vi.fn(),
  };
  const runtime = createLanguageRuntime({
    environment,
    catalogs: { en: { greeting: 'Hello' }, 'zh-CN': { greeting: '你好' } },
  });
  return { runtime, environment, connections: () => connections };
};

const Probe = () => {
  const { effectiveLocale } = useLanguage();
  const { i18n } = useLingui();
  const [count, setCount] = useState(0);
  return <button lang={effectiveLocale} onClick={() => setCount(count + 1)}>{i18n._('greeting')} {count}</button>;
};

describe('language provider', () => {
  it('changes copy without replacing state or focused elements', () => {
    const f = fixture();
    act(() => root.render(<LanguageProvider runtime={f.runtime}><Probe /></LanguageProvider>));
    const button = container.querySelector('button')!;
    act(() => button.click());
    button.focus();
    act(() => f.runtime.setPreference('zh-CN'));
    expect(container.querySelector('button')).toBe(button);
    expect(button.textContent).toBe('你好 1');
    expect(button.lang).toBe('zh-CN');
    expect(document.activeElement).toBe(button);
    expect(document.documentElement.lang).toBe('zh-CN');
  });

  it('keeps one connection through StrictMode and removes it on unmount', () => {
    const f = fixture();
    act(() => root.render(<StrictMode><LanguageProvider runtime={f.runtime}><Probe /></LanguageProvider></StrictMode>));
    expect(f.connections()).toBe(1);
    act(() => root.unmount());
    expect(f.connections()).toBe(0);
    root = createRoot(container);
  });

  it('initializes document and catalogs before the first child render', () => {
    const f = fixture();
    vi.spyOn(f.environment, 'readPreference').mockReturnValue('zh-CN');
    const observations: string[] = [];
    const FirstRender = () => {
      const { effectiveLocale } = useLanguage();
      observations.push(`${effectiveLocale}:${document.documentElement.lang}:${f.runtime.i18n._('greeting')}`);
      return null;
    };
    act(() => root.render(<LanguageProvider runtime={f.runtime}><FirstRender /></LanguageProvider>));
    expect(observations[0]).toBe('zh-CN:zh-CN:你好');
  });

  it('provides the application runtime initialized by the bootstrap', () => {
    initializeLanguage();
    expect(i18n).toBe(applicationLanguageRuntime.i18n);
    const DefaultProbe = () => <span>{useLanguage().effectiveLocale}</span>;
    act(() => root.render(<LanguageProvider><DefaultProbe /></LanguageProvider>));
    expect(container.textContent).toBe(applicationLanguageRuntime.getSnapshot().effectiveLocale);
  });

  it('rejects a language hook outside its provider', () => {
    expect(() => renderToString(<Probe />)).toThrow('useLanguage requires a LanguageProvider.');
  });
});
