import { afterEach } from 'vitest'
import { cleanup, configure } from '@testing-library/react'

// Unmount rendered React trees after each test so queries don't see stale DOM.
afterEach(cleanup)

// waitFor / findBy* default to a 1s budget, which is not a statement about the code — it is a bet
// that a machine running the whole suite in parallel will schedule this one microtask promptly. It
// lost that bet roughly one run in eight: a provider whose fetch had not resolved yet still showed
// its pre-fetch fallback, and the assertion reported the fallback as if it were the final value.
// The failure roamed between files, because whichever one lost the CPU that run was the one to
// fail. Raising the ceiling costs a passing test nothing (waitFor returns as soon as the assertion
// holds) and only lengthens a genuine failure.
configure({ asyncUtilTimeout: 5000 })

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
