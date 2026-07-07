import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import QueueTable from './QueueTable'

// Smoke test: the queue page must mount without crashing (it renders on load, before any
// interaction). Guards against a render-time bug being mistaken for a caching/blank-page
// issue. Empty data is enough to exercise the initial render path.
vi.mock('../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url.includes('/queue')) return Promise.resolve({ running: 0, waiting: 0, scheduled: 0, budget: 0 })
      if (url.includes('/targets')) return Promise.resolve({ targets: [] })
      return Promise.resolve({ jobs: [] })
    },
    post: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true, user: 'alice' }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('QueueTable', () => {
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
})
