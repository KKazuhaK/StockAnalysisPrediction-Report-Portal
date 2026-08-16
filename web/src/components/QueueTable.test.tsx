import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import QueueTable from './QueueTable'
import { INPUT_VALUE_MAX } from '../lib/batchUi'
import type { BatchJob } from '../api/types'

// Mutable so a test can inject jobs into the mocked API. vi.hoisted keeps it reachable from
// the hoisted vi.mock factory below. `hold` makes every polled GET hang, which is what a slow
// link looks like for the seconds that matter: the page is up and nothing has answered yet.
const store = vi.hoisted(() => ({ jobs: [] as BatchJob[], hold: false, fail: '', unchanged: false }))
const forgetTags = vi.hoisted(() => vi.fn())

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
  errText: (e: unknown) => String((e as Error)?.message ?? e),
}))
// The two polled endpoints go through the conditional-GET helper (304 on an unchanged queue), so
// that is what a test has to answer. `store.unchanged` turns every answer into UNCHANGED, because
// what the table does with a 304 it has no data for is one of the things this suite pins; the
// helper's own revalidation logic is conditionalGet.test.ts's.
const UNCHANGED = vi.hoisted(() => Symbol('unchanged'))
vi.mock('../lib/conditionalGet', () => ({
  UNCHANGED,
  forgetTags,
  getIfChanged: (url: string) => {
    if (store.hold) return new Promise(() => {})
    if (store.fail) return Promise.reject(new Error(store.fail))
    if (store.unchanged) return Promise.resolve(UNCHANGED)
    return String(url).includes('/queue')
      ? Promise.resolve({ running: 0, waiting: 0, scheduled: 0, budget: 0 })
      : Promise.resolve({ jobs: store.jobs })
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

const mount = () =>
  render(
    <App>
      <MemoryRouter>
        <QueueTable showStats />
      </MemoryRouter>
    </App>,
  )

describe('QueueTable', () => {
  beforeEach(() => {
    store.jobs = []
    store.hold = false
    store.fail = ''
    store.unchanged = false
    forgetTags.mockClear()
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

  // The bug this guards: on a slow link the page arrived fully drawn and said "Queue is empty"
  // over five tiles reading zero, seconds before anything had been asked of the server. Both
  // halves are statements about the queue, and neither was one the server had made.
  it('says it is loading instead of declaring the queue empty', async () => {
    store.hold = true
    const { container } = mount()
    expect(await screen.findByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('queue.empty')).toBeNull()
    expect(container.querySelector('.ant-table')).toBeNull()
    // …and the stat tiles hold a dash rather than a zero they cannot vouch for.
    expect(screen.queryAllByText('—').length).toBe(5)
    expect(screen.queryByText('0')).toBeNull()
  })

  it('shows the empty state once the server has actually answered with no runs', async () => {
    mount()
    expect(await screen.findByText('queue.empty')).toBeTruthy()
    expect(screen.queryByText('common.loading')).toBeNull()
  })

  // The ETag store is module-global and outlives any one mount, so a component that holds nothing
  // can be told "nothing changed" — about data it has never had. Two guards, both pinned here.
  it('asks unconditionally for what it is not holding', async () => {
    mount()
    await screen.findByText('queue.empty')
    const asked = forgetTags.mock.calls.map((c) => String(c[0]))
    expect(asked.some((u) => u.includes('/api/admin/batch/jobs'))).toBe(true)
    expect(asked).toContain('/api/admin/batch/queue')
  })

  it('does not open the gate on a 304 for rows it never received', async () => {
    store.unchanged = true
    mount()
    // The answer said "unchanged since the tag you sent" — and this mount sent nothing it holds.
    // Declaring the queue empty off that is the original bug wearing a 304.
    expect(await screen.findByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('queue.empty')).toBeNull()
    expect(screen.queryAllByText('—').length).toBe(5)
  })

  // A first load that never lands must not fall through to the empty state either — with
  // auto-refresh off nothing would ever correct it.
  it('reports a failed first load, with a retry, rather than an empty queue', async () => {
    store.fail = 'boom'
    mount()
    expect(await screen.findByText('common.loadFailedContent')).toBeTruthy()
    expect(screen.getByText('boom')).toBeTruthy()
    expect(screen.getByText('common.retry')).toBeTruthy()
    expect(screen.queryByText('queue.empty')).toBeNull()
  })
})
