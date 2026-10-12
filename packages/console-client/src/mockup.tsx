import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { HashRouter } from 'react-router-dom';
import { MockupConsoleApp } from './mockup/MockupConsoleApp';
import { LanguageProvider } from './i18n/provider';
import { initializeLanguage } from './i18n/runtime';
import './styles/console.css';

initializeLanguage();
const root = document.getElementById('root');
if (!root) throw new Error('Mockup root element is missing.');
createRoot(root).render(
  <StrictMode>
    <LanguageProvider><HashRouter><MockupConsoleApp /></HashRouter></LanguageProvider>
  </StrictMode>,
);
