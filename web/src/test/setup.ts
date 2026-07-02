import { afterEach } from 'vitest'
import { cleanup } from '@testing-library/react'

// Unmount rendered React trees after each test so queries don't see stale DOM.
afterEach(cleanup)
