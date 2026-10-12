import { defineConfig } from 'vitest/config';
import { consolePlugins } from './vite.config.ts';

export default defineConfig({
  plugins: consolePlugins(),
  test: {
    environment: 'jsdom',
    environmentOptions: { jsdom: { url: 'http://localhost:5173/' } },
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}', 'scripts/**/*.test.ts'],
    exclude: ['node_modules/**', 'dist/**'],
    testTimeout: 10_000,
    hookTimeout: 10_000,
    maxWorkers: 3,
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/**/*.test.*', 'src/test/**', 'src/mockup/**', 'src/mockup.tsx', 'src/**/*.d.ts'],
      reporter: ['text', 'json-summary', 'html'],
      reportsDirectory: '.tmp/coverage',
      thresholds: { statements: 90, lines: 90, functions: 85, branches: 80 },
    },
  },
});
