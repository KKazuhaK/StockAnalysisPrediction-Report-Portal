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
      '/app-assets': 'http://localhost:8790', // installed iframe apps' static files (ADR 0003)
      '/site-assets': 'http://localhost:8790',
      '/report': 'http://localhost:8790',
      '/healthz': 'http://localhost:8790',
      '/manifest.webmanifest': 'http://localhost:8790',
      '/pwa-icon': 'http://localhost:8790',
    },
  },
  build: {
    // Keep emptyOutDir false so the committed .gitkeep survives — that lets
    // `go build` compile the embed on a fresh clone before the SPA is built.
    outDir: '../internal/web/dist',
    emptyOutDir: false,
    chunkSizeWarningLimit: 1600,
    rollupOptions: {
      output: {
        // Keep React in a stable vendor chunk. Ant Design is deliberately left to
        // Rollup: forcing every antd/rc module into one manual chunk made code used
        // only by lazy admin routes part of the initial module-preload graph.
        // Route-only dependencies (markdown, dnd-kit, admin-only antd components)
        // can now remain behind their route boundary.
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('/react/') || id.includes('/react-dom/') || id.includes('/react-router') || id.includes('/scheduler/')) return 'react'
        },
      },
    },
  },
  test: {
    // jsdom for component tests; explicit imports (no globals) keep tsc happy.
    environment: 'jsdom',
    globals: false,
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
  },
})
