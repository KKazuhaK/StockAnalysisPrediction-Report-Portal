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
        // Split the big, stable vendor libs into their own chunks so an app-code
        // release doesn't invalidate them — the browser keeps antd/react cached
        // across versions. Route-only deps (markdown, dnd-kit) are left to Rollup,
        // which keeps them in the lazy route chunks that import them.
        manualChunks(id) {
          if (!id.includes('node_modules')) return
          if (id.includes('/antd/') || id.includes('/@ant-design/') || id.includes('/rc-')) return 'antd'
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
