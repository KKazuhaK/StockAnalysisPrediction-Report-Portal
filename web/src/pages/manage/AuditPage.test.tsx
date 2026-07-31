import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { App } from 'antd'
import AuditPage from './AuditPage'

// The log is only useful if a row can be read without cross-referencing anything: who, what, which
// object, when. Two of those need help — a machine caller has no username, and an OU id means
// nothing on screen.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() }))
vi.mock('../../api/client', () => ({
  api: apiMock,
  ApiError: class extends Error {},
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, fb?: unknown) => (typeof fb === 'string' ? fb : k) }),
}))

const RESP = {
  total: 2,
  actions: ['report.read', 'grant.change'],
  ou_names: { '7': '客户A' },
  items: [
    { id: 2, at: '2026-08-01 09:00:00', actor: '', actor_ou: 0, action: 'report.read',
      target_type: 'report', target_id: '42', detail: '{"symbol":"600519"}' },
    { id: 1, at: '2026-08-01 08:00:00', actor: 'client@corp.example', actor_ou: 7,
      action: 'grant.change', target_type: 'version', target_id: '对外版',
      detail: '{"before":[],"after":["u:client@corp.example"]}' },
  ],
}

const mount = () =>
  render(
    <App>
      <AuditPage />
    </App>,
  )

describe('AuditPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.get.mockResolvedValue(RESP)
  })

  it('names the OU the actor was in, rather than showing a bare id', async () => {
    mount()
    expect(await screen.findByText('客户A')).toBeTruthy()
  })

  it('says a machine acted instead of leaving the actor blank', async () => {
    mount()
    // An empty cell reads as a bug; "(API token)" reads as a fact.
    expect(await screen.findByText('audit.machine')).toBeTruthy()
  })

  it('shows the object and the detail, so a line is readable on its own', async () => {
    mount()
    expect(await screen.findByText(/对外版/)).toBeTruthy()
    expect(screen.getByText(/600519/)).toBeTruthy()
    // A grant change carries both sides — the current state cannot answer "when did they gain it".
    expect(screen.getByText(/"before":\[\]/)).toBeTruthy()
  })

  it('asks the server for a page, not the whole table', async () => {
    mount()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    expect(String(apiMock.get.mock.calls[0][0])).toContain('limit=50')
  })
})
