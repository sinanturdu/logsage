/// <reference types="vitest/config" />
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// The dev server proxies /api to cmd/api, so the browser talks to one
// origin and the session cookie is first-party. changeOrigin stays false:
// the API then sees Host: localhost:5173, the same host as the Origin
// header, and its CSRF check accepts the request without ALLOWED_ORIGINS.
// /config.json comes from cmd/api too, so development reads the same runtime config as production.
const api = {
  '/api': { target: 'http://127.0.0.1:8083', changeOrigin: false },
  '/config.json': { target: 'http://127.0.0.1:8083', changeOrigin: false },
}

export default defineConfig({
  plugins: [react()],
  server: { port: 5173, strictPort: true, proxy: api },
  preview: { port: 4173, strictPort: true, proxy: api },
  test: {
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    restoreMocks: true,
    unstubGlobals: true,
    // Lets style tests read app.css (for example: no upper-case labels).
    css: { include: [/app\.css/] },
  },
})
