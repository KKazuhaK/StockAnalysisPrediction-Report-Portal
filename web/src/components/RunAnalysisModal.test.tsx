import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import RunAnalysisModal from './RunAnalysisModal'

// Every GET the modal makes hangs, which is what a slow link looks like for the seconds that
// matter: the modal is already open and the answers are not back yet.
const pending = vi.hoisted(() => ({ hold: true }))
vi.mock('../api/client', () => ({
  api: { get: () => (pending.hold ? new Promise(() => {}) : Promise.resolve({})), post: () => Promise.resolve({}) },
  errText: (e: unknown) => String(e),
}))
vi.mock('../auth', () => ({ useAuth: () => ({ email: '', mailEnabled: false, admin: true, user: 'alice' }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))
vi.mock('react-router', () => ({ useNavigate: () => vi.fn() }))

// The shell warms the two gating GETs while it is idle; this is the payoff.
vi.mock('../lib/prefetch', () => ({
  readPrefetched: (url: string) =>
    warm.on
      ? url.endsWith('/targets')
        ? { targets: [{ id: 1, name: 'Daily', plugin_slug: 'dify', created_at: '', mode: 'workflow', inputs: [] }] }
        : { presets: [], default_mode: 'now' }
      : undefined,
  prefetch: () => Promise.resolve(),
}))
const warm = vi.hoisted(() => ({ on: false }))

describe('RunAnalysisModal with the shell-warmed answers already in hand', () => {
  it('opens on the form, with no spinner at all', async () => {
    warm.on = true
    render(
      <App>
        <RunAnalysisModal open onClose={() => {}} />
      </App>,
    )
    expect(await screen.findByText('run.workflow')).toBeTruthy()
    expect(screen.queryByText('run.loading')).toBeNull()
    warm.on = false
  })
})

describe('RunAnalysisModal while its data is still loading', () => {
  it('says it is loading instead of showing an empty form', async () => {
    const { container } = render(
      <App>
        <RunAnalysisModal open onClose={() => {}} />
      </App>,
    )
    expect(await screen.findByText('run.loading')).toBeTruthy()
    // The killer is not the emptiness, it is the claim: "no workflows configured" is a
    // statement about the server, and until the server has answered it is not one we can make.
    expect(screen.queryByText('run.noTargets')).toBeNull()
    expect(container.querySelector('.ant-select')).toBeNull()
  })
})
