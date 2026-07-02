/// <reference types="vitest/config" />
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// In dev, proxy API / downloads / health check to the Go backend (:8790). The
// production build is written to ../internal/web/dist for //go:embed.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:8790',
      '/report': 'http://localhost:8790',
      '/healthz': 'http://localhost:8790',
    },
  },
  build: {
    // Keep emptyOutDir false so the committed .gitkeep survives — that lets
    // `go build` compile the embed on a fresh clone before the SPA is built.
    outDir: '../internal/web/dist',
    emptyOutDir: false,
    chunkSizeWarningLimit: 1600,
  },
  test: {
    // jsdom for component tests; explicit imports (no globals) keep tsc happy.
    environment: 'jsdom',
    globals: false,
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
