import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import RunQueueSettingsPage from './RunQueueSettingsPage'

const server = vi.hoisted(() => ({ resolve: null as null | ((v: unknown) => void), reject: null as null | ((e: unknown) => void) }))
vi.mock('../../api/client', () => ({
  api: {
    get: () =>
      new Promise((resolve, reject) => {
        server.resolve = resolve
        server.reject = reject
      }),
    post: () => Promise.resolve({}),
  },
  errText: () => 'boom',
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))
vi.mock('./RunPresetsEditor', () => ({ default: () => <div>presets</div> }))

const renderPage = () =>
  render(
    <App>
      <RunQueueSettingsPage />
    </App>,
  )

describe('RunQueueSettingsPage before its configuration arrives', () => {
  // The fields are initialised to plausible-looking numbers — 1 run at a time, priority 50,
  // weights of 1000 — and the form is live. Showing them as if they were the stored settings
  // means an admin on a slow link can read a value the server never sent, and save it back.
  it('shows neither the settings nor the save button until the server has answered', async () => {
    renderPage()
    await screen.findByText('common.loading')
    expect(screen.queryByText('batch.admin.maxJobs')).toBeNull()
    expect(screen.queryByText('common.save')).toBeNull()

    server.resolve?.({ max_jobs: 8, prio_w_base: 1000, prio_w_age: 1000, prio_w_fair: 1000, prio_age_hours: 24, prio_fair_halflife_hours: 168 })
    await waitFor(() => expect(screen.getByText('batch.admin.maxJobs')).toBeTruthy())
  })

  it('reports a failed load instead of presenting its defaults as the configuration', async () => {
    renderPage()
    await screen.findByText('common.loading')
    server.reject?.(new Error('offline'))
    await waitFor(() => expect(screen.getByText('common.loadFailed')).toBeTruthy())
    expect(screen.queryByText('common.save')).toBeNull()
  })
})
