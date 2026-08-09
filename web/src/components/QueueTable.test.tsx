import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import QueueTable from './QueueTable'
import { INPUT_VALUE_MAX } from '../lib/batchUi'
import type { BatchJob } from '../api/types'

// Mutable so a test can inject jobs into the mocked API. vi.hoisted keeps it reachable from
// the hoisted vi.mock factory below.
const store = vi.hoisted(() => ({ jobs: [] as BatchJob[] }))

// Smoke test: the queue page must mount without crashing (it renders on load, before any
// interaction). Guards against a render-time bug being mistaken for a caching/blank-page
// issue. Empty data is enough to exercise the initial render path.
vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url.includes('/queue')) return Promise.resolve({ running: 0, waiting: 0, scheduled: 0, budget: 0 })
      if (url.includes('/targets')) return Promise.resolve({ targets: [] })
      return Promise.resolve({ jobs: store.jobs })
    },
    post: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true, user: 'alice' }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const job = (over: Partial<BatchJob>): BatchJob => ({
  id: 1,
  target_id: 1,
  status: 'running',
  concurrency: 1,
  max_retries: 0,
  total: 1,
  succeeded: 0,
  partial: 0,
  failed: 0,
  created_by: 'alice',
  created_at: '2026-07-12 00:00:00',
  started_at: '',
  finished_at: '',
  ...over,
})

describe('QueueTable', () => {
  beforeEach(() => {
    store.jobs = []
  })

  it('mounts and renders the queue card without crashing', async () => {
    render(
      <App>
        <MemoryRouter>
          <QueueTable showStats />
        </MemoryRouter>
      </App>,
    )
    expect(await screen.findByText('queue.title')).toBeTruthy()
  })

  // A chat/agent run carries a full `query=<prompt>` in its inputs; the workflow column must
  // clamp it so one long run can't stretch the row to an unbounded height. The value is now
  // cut in the markup itself, not only by CSS — that is also what keeps the hover tooltip
  // from growing past the viewport. Regression guard for the "infinitely long queue row" bug.
  it('clamps a long chat query in the workflow column instead of rendering it full-height', async () => {
    const longQuery = 'Q'.repeat(1200)
    store.jobs = [job({ inputs: JSON.stringify({ query: longQuery, symbol: '301539' }) })]
    render(
      <App>
        <MemoryRouter>
          <QueueTable showStats />
        </MemoryRouter>
      </App>,
    )
    // …and the clamp is per value, so the entry after the runaway prompt is still visible.
    // (the query normalizes the entry separator's double space down to one)
    const preview = await screen.findByText(`query=${'Q'.repeat(INPUT_VALUE_MAX)}… symbol=301539`)
    expect(preview.className).toMatch(/ant-typography-ellipsis/)
  })
})
