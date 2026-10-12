import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { lingui } from '@lingui/vite-plugin';

export const consolePlugins = () => [
  react(),
  lingui({ macroTransform: true, failOnMissing: 'catalog', failOnCompileError: true }),
];

const target = process.env.CONSOLE_API_TARGET ?? 'http://127.0.0.1:8090';
const api = new URL(target);
if (!['http:', 'https:'].includes(api.protocol) || api.username || api.password || api.search || api.hash || api.pathname !== '/') {
  throw new Error('CONSOLE_API_TARGET must be an HTTP(S) origin without credentials or a path.');
}
const portText = process.env.CONSOLE_CLIENT_PORT ?? '5173';
const port = Number(portText);
if (!/^\d+$/.test(portText) || !Number.isInteger(port) || port < 1 || port > 65535) {
  throw new Error('CONSOLE_CLIENT_PORT must be a port between 1 and 65535.');
}

export default defineConfig({
  plugins: consolePlugins(),
  server: {
    host: '127.0.0.1',
    port,
    strictPort: true,
    proxy: {
      '/auth/v1': { target, changeOrigin: false },
      '/api/v1': { target, changeOrigin: false },
    },
  },
  build: { outDir: 'dist', sourcemap: false },
});
