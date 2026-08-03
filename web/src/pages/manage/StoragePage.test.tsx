import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import StoragePage from './StoragePage'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('../../api/client', () => ({ api: apiMock }))

// Same convention as every other test file here: an interpolated string keeps its arguments, so a
// test can assert the NUMBERS a confirm dialog states, not merely that a dialog appeared.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const config = {
  freq: 'daily',
  time: '03:00',
  weekday: 1,
  monthday: 1,
  batch_enabled: true,
  batch_days: 90,
  tokens_enabled: false,
  tokens_grace_days: 30,
  reports_enabled: false,
  reports_days: 730,
  batch_floor: 7,
  reports_floor: 365,
  last_run_period: '',
  last_result: null,
}

// Only batch is eligible, so exactly one clean button renders.
const usage = {
  db_bytes: 2048,
  categories: [
    { key: 'batch', rows: 12, bytes: 1000, eligible: 3, oldest: '', newest: '' },
    { key: 'tokens', rows: 2, bytes: 40, eligible: 0, oldest: '', newest: '' },
    { key: 'reports', rows: 100, bytes: 500000, eligible: 0, oldest: '', newest: '' },
    { key: 'chat', rows: 5, bytes: 20, eligible: 0, oldest: '', newest: '' },
  ],
}

const history = {
  runs: [{ id: 1, ran_at: '2026-07-11 03:00:00', trigger: 'manual', dry_run: false, ok: false, error: 'boom', batch_deleted: 0, tokens_deleted: 0, reports_deleted: 0, duration_ms: 3 }],
}

function renderPage() {
  return render(
    <App>
      <StoragePage />
    </App>,
  )
}

describe('StoragePage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/cleanup/config')) return Promise.resolve({ ...config })
      if (url.includes('/cleanup/usage')) return Promise.resolve({ ...usage })
      if (url.includes('/cleanup/history')) return Promise.resolve({ ...history })
      return Promise.resolve({})
    })
    apiMock.post.mockResolvedValue({ batch: 3, tokens: 0, reports: 2, ok: true, at: '', trigger: 'preview', dry_run: true, error: '', duration_ms: 1 })
  })

  it('renders the usage dashboard: category cards, eligible/nothing states, and a failed history row', async () => {
    renderPage()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/cleanup/usage'))
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/cleanup/config')
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/cleanup/history')
    // every category has a card
    expect((await screen.findAllByText('storage.cat.batch')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('storage.cat.reports').length).toBeGreaterThan(0)
    // eligible → an "N to clean" tag; a not-eligible category shows "nothing to clean"; chat is view-only
    expect(screen.getByText(/storage\.eligibleN/)).toBeTruthy()
    expect(screen.getAllByText('storage.noCleanup').length).toBeGreaterThan(0)
    expect(screen.getByText('storage.ruleChat')).toBeTruthy()
    // failed history row surfaces its error text
    expect(screen.getByText('boom')).toBeTruthy()
  })

  it('the self-describing clean button previews then opens a confirm', async () => {
    const user = userEvent.setup()
    renderPage()
    const cleanBtn = await screen.findByText(/storage\.act/) // only batch is eligible
    await user.click(cleanBtn)
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/cleanup/preview', { targets: ['batch'] }))
    expect((await screen.findAllByText('storage.confirmTitle')).length).toBeGreaterThan(0)
  })

  // The confirm restates the live count, and a destructive confirm that states the WRONG count is
  // worse than one that states none: it reads as a checked fact. The count was picked with a
  // two-way ternary, so every target that is not batch showed the TOKENS number — including audit,
  // added later, whose rows are the one thing on this page that cannot be regenerated.
  it('counts the category being cleaned, not whichever one the ternary fell through to', async () => {
    const user = userEvent.setup()
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/cleanup/config')) return Promise.resolve({ ...config, audit_enabled: true, audit_days: 90 })
      if (url.includes('/cleanup/usage'))
        return Promise.resolve({
          ...usage,
          categories: [...usage.categories, { key: 'audit', rows: 900, bytes: 700, eligible: 640, oldest: '', newest: '' }],
        })
      if (url.includes('/cleanup/history')) return Promise.resolve({ ...history })
      return Promise.resolve({})
    })
    apiMock.post.mockResolvedValue({ batch: 3, tokens: 0, reports: 2, audit: 640, ok: true, at: '', trigger: 'preview', dry_run: true, error: '', duration_ms: 1 })
    renderPage()
    const buttons = await screen.findAllByText(/storage\.act/)
    await user.click(buttons[buttons.length - 1]) // audit is the last eligible category
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/cleanup/preview', { targets: ['audit'] }))
    // 640 is the audit count; 0 is the tokens count the old ternary would have shown.
    await waitFor(() => expect(screen.getByText(/storage\.confirmBody.*"n":640/)).toBeTruthy())
  })

  it('enabling reports auto-delete previews the live count and opens the danger confirm', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findAllByText('storage.cat.batch')
    // The reports switch is the LAST one: it lives in its own danger section below the others, so
    // this stays correct when a new retention target is added above it (audit was).
    const switches = screen.getAllByRole('switch')
    await user.click(switches[switches.length - 1])
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/cleanup/preview', { targets: ['reports'] }))
    expect((await screen.findAllByText('storage.confirmReportsTitle')).length).toBeGreaterThan(0)
  })

  it('saves the full config payload', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findAllByText('storage.cat.batch')
    await user.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith(
        '/api/admin/cleanup/config',
        expect.objectContaining({ freq: 'daily', batch_enabled: true, batch_days: 90, reports_enabled: false, reports_days: 730 }),
      ),
    )
  })
})
