import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

// Unmount rendered React trees after each test so queries don't see stale DOM.
afterEach(cleanup)

// jsdom in this runtime ships without Web Storage; install a minimal in-memory
// localStorage so modules that persist prefs (reader / prefs) work under test.
if (typeof window !== 'undefined' && !window.localStorage) {
  const mem = new Map<string, string>()
  const storage: Storage = {
    get length() {
      return mem.size
    },
    clear: () => mem.clear(),
    getItem: (k: string) => (mem.has(k) ? (mem.get(k) as string) : null),
    key: (i: number) => Array.from(mem.keys())[i] ?? null,
    removeItem: (k: string) => {
      mem.delete(k)
    },
    setItem: (k: string, v: string) => {
      mem.set(k, String(v))
    },
  }
  Object.defineProperty(window, 'localStorage', { value: storage, configurable: true })
}

// jsdom has no matchMedia; antd's responsive hooks (Grid.useBreakpoint, used by Tabs /
// Table / Row) call it on mount. Install an inert, never-matching implementation so
// components that rely on it render under test.
if (typeof window !== 'undefined' && !window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (query: string): MediaQueryList => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {}, // deprecated, but antd's observer still calls it
      removeListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}

// jsdom has no ResizeObserver; antd 6 routes far more components through
// rc-resize-observer (it observes each element's box on mount) than v5 did, so its
// absence now throws during render. Install an inert stub — layout measurements are
// irrelevant to these component assertions.
if (typeof globalThis !== 'undefined' && !('ResizeObserver' in globalThis)) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  ;(globalThis as { ResizeObserver?: unknown }).ResizeObserver = ResizeObserverStub
}
