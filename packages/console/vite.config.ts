import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  base: '/console/',
  plugins: [react()],
  server: {
    proxy: {
      '/auth': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      '/admin': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
