import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { BrowserRouter } from 'react-router-dom';
import { ConsoleApp } from './app/ConsoleApp';
import { SessionProvider } from './auth';
import { LanguageProvider } from './i18n/provider';
import { initializeLanguage } from './i18n/runtime';
import './styles/console.css';

initializeLanguage();
const root = document.getElementById('root');
if (!root) throw new Error('Console root element is missing.');
createRoot(root).render(
  <StrictMode>
    <LanguageProvider>
      <BrowserRouter>
        <SessionProvider><ConsoleApp /></SessionProvider>
      </BrowserRouter>
    </LanguageProvider>
  </StrictMode>,
);
