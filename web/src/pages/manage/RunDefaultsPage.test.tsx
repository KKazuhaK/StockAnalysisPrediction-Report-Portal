import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import RunDefaultsPage from './RunDefaultsPage'

// Three GETs back this page — the stored defaults, the workflow list and the preset windows —
// and `hold` keeps them all on the wire, which is what a slow link looks like for the seconds
// that matter.
const server = vi.hoisted(() => ({
  hold: false,
  config: {} as Record<string, unknown>,
  targets: [] as Array<Record<string, unknown>>,
  presets: [] as Array<Record<string, unknown>>,
  fail: false,
  posted: [] as Array<Record<string, unknown>>,
}))

vi.mock('../../api/client', () => ({
  api: {
    get: (url: string) => {
      if (server.hold) return new Promise(() => {})
      if (server.fail) return Promise.reject(new Error('offline'))
      if (url.endsWith('/config')) return Promise.resolve(server.config)
      if (url.endsWith('/targets')) return Promise.resolve({ targets: server.targets })
      return Promise.resolve({ presets: server.presets })
    },
    post: (_url: string, body: Record<string, unknown>) => {
      server.posted.push(body)
      return Promise.resolve({})
    },
  },
  errText: () => 'boom',
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const target = (id: number, name: string, surfaces?: string[]) => ({
  id,
  name,
  plugin_slug: 'dify',
  created_at: '',
  mode: 'workflow',
  ...(surfaces ? { surfaces } : {}),
})

const renderPage = () =>
  render(
    <MemoryRouter>
      <App>
        <RunDefaultsPage />
      </App>
    </MemoryRouter>,
  )

beforeEach(() => {
  server.hold = false
  server.fail = false
  server.config = {}
  server.targets = []
  server.presets = []
  server.posted = []
})

describe('RunDefaultsPage before its answers arrive', () => {
  // Same rule as the run/queue page: every field here starts at a plausible value and the form is
  // live, so showing it early means an admin can read a default the server never sent — and save
  // it back over a real one.
  it('shows neither the form nor the save button until the server has answered', async () => {
    server.hold = true
    renderPage()
    await screen.findByText('common.loading')
    expect(screen.queryByText('runDefaults.workflow')).toBeNull()
    expect(screen.queryByText('common.save')).toBeNull()
  })

  it('reports a failed load instead of presenting its own blanks as the configuration', async () => {
    server.fail = true
    renderPage()
    await waitFor(() => expect(screen.getByText('common.loadFailed')).toBeTruthy())
    expect(screen.queryByText('common.save')).toBeNull()
  })
})

describe('RunDefaultsPage', () => {
  it('posts the zeros that mean "no default" rather than omitting them', async () => {
    // Clearing a default has to be expressible: an omitted field leaves the stored one alone, so
    // "no default" must travel as an explicit 0 / false.
    server.config = { run_default_target_id: 7, run_default_mode: 'now', run_default_preset_id: 3 }
    server.targets = [target(7, 'Daily')]
    server.presets = [{ id: 3, label: 'Off-peak', freq: 'daily', intervals: [], on_overrun: 'next', enabled: true, invert: false, ord: 0 }]
    renderPage()
    await screen.findByText('runDefaults.workflow')

    // The workflow picker: open it and take the "no default" entry.
    const user = userEvent.setup()
    await user.click(screen.getByText('Daily'))
    await user.click(await screen.findByTitle('runDefaults.none'))
    await user.click(screen.getByText('common.save'))

    await waitFor(() => expect(server.posted).toHaveLength(1))
    expect(server.posted[0]).toMatchObject({
      run_default_target_id: 0,
      run_default_preset_id: 3, // untouched fields still travel, so a save is a full statement
      run_default_mode: 'now',
      run_default_idle: false,
      run_default_notify: false,
    })
  })

  it('still lists a saved workflow that has since been hidden from the run dialog', async () => {
    // The alternative is a picker that shows "no default" for a portal that has one — the admin
    // would have no way to see, keep, or clear the choice they made.
    server.config = { run_default_target_id: 7 }
    server.targets = [target(7, 'Daily', ['batch']), target(8, 'Weekly')]
    renderPage()
    await screen.findByText('runDefaults.workflow')
    await waitFor(() => expect(screen.getByTitle('Daily（runDefaults.hiddenOnRun）')).toBeTruthy())
  })
})
