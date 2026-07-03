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
