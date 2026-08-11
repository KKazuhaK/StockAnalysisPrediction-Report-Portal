import { describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import RunAnalysisModal from './RunAnalysisModal'
import type { PluginInput } from '../api/types'

// Every GET the modal makes hangs, which is what a slow link looks like for the seconds that
// matter: the modal is already open and the answers are not back yet.
const pending = vi.hoisted(() => ({ hold: true }))
const posted = vi.hoisted(() => ({ calls: [] as Array<Record<string, unknown>> }))
vi.mock('../api/client', () => ({
  api: {
    get: () => (pending.hold ? new Promise(() => {}) : Promise.resolve({})),
    post: (_url: string, body: Record<string, unknown>) => {
      posted.calls.push(body)
      return Promise.resolve({ job_id: 9 })
    },
    upload: (_url: string, body: FormData) =>
      Promise.resolve({ ok: true, file_id: 'dify-1', name: (body.get('file') as File).name, size: 1 }),
  },
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
        ? { targets: [{ id: 1, name: 'Daily', plugin_slug: 'dify', created_at: '', mode: 'workflow', inputs: warm.inputs }] }
        : { presets: [], default_mode: 'now' }
      : undefined,
  prefetch: () => Promise.resolve(),
}))
const warm = vi.hoisted(() => ({ on: false, inputs: [] as PluginInput[] }))

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

// The declared type of an input used to be dropped on the way to the form, so every field — a
// prompt, a year, a fixed choice, an attachment — was drawn as the same one-line text box.
describe('RunAnalysisModal draws each input as its declared type', () => {
  // The dialog is a portal, so it lands beside the render container rather than inside it.
  const q = (sel: string) => document.body.querySelector(sel)
  const qq = (sel: string) => document.body.querySelectorAll(sel)

  const open = async (inputs: PluginInput[]) => {
    warm.on = true
    warm.inputs = inputs
    render(
      <App>
        <RunAnalysisModal open initialTargetId={1} onClose={() => {}} />
      </App>,
    )
    await screen.findByText('run.workflow')
    warm.on = false
  }

  it('gives a paragraph a textarea, a number a spinner and a select its options', async () => {
    await open([
      { key: 'note', label: 'Note', type: 'paragraph' },
      { key: 'year', label: 'Year', type: 'number' },
      { key: 'mode', label: 'Mode', type: 'select', options: ['fast', 'deep'] },
    ])
    await waitFor(() => expect(q('textarea')).toBeTruthy())
    expect(q('.ant-input-number')).toBeTruthy()
    // Two Selects: the workflow picker plus the declared one.
    expect(qq('.ant-select')).toHaveLength(2)
  })

  it('falls back to a text box for a select that arrived without options', async () => {
    // An empty menu is not a control — it is a field nobody can fill.
    await open([{ key: 'mode', label: 'Mode', type: 'select' }])
    await waitFor(() => expect(q('#mode')).toBeTruthy())
    expect(qq('.ant-select')).toHaveLength(1)
  })

  it('uploads a file input and submits the row with the Dify id, not the file', async () => {
    const user = userEvent.setup()
    posted.calls = []
    await open([
      { key: 'symbol', label: 'Symbol' },
      // Required, because a required file field is where the validation rule bites: a rule with no
      // type validates the uploaded list as a string and refuses it, upload or no upload.
      { key: 'docs', label: 'Docs', type: 'file-list', required: true },
    ])
    await waitFor(() => expect(q('input[type="file"]')).toBeTruthy())
    await user.type(q('#symbol') as HTMLInputElement, '301539')
    await user.upload(q('input[type="file"]') as HTMLInputElement, new File(['hi'], 'a.pdf', { type: 'application/pdf' }))
    await screen.findByText('a.pdf')
    await user.click(screen.getByText('run.run'))
    await waitFor(() => expect(posted.calls).toHaveLength(1))
    expect(posted.calls[0].rows).toEqual([{ symbol: '301539', docs: '["dify-1"]' }])
  })
})
