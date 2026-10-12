import { defineConfig } from '@lingui/conf';
import { formatter } from '@lingui/format-po';

export default defineConfig({
  sourceLocale: 'en',
  locales: ['en', 'zh-CN'],
  catalogs: [{
    path: '<rootDir>/src/i18n/locales/{locale}/messages',
    include: ['<rootDir>/src'],
    exclude: ['**/*.test.*', '**/test/**', '**/mockup/**', '**/locales/**'],
  }],
  format: formatter({ lineNumbers: false }),
  orderBy: 'message',
});
