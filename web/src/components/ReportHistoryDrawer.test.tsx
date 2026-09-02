import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App as AntdApp } from 'antd'
import { MemoryRouter, Route, Routes } from 'react-router'
import ReportEditorPage from '../pages/ReportEditorPage'

// The history drawer, tested through the editor it hangs off, because the two things worth pinning
// are both about the seam: that opening a history cannot disturb an unsaved draft, and that a
// restore is computed against the token the EDITOR holds rather than whatever the report says now.

const state = {
  form: {} as Record<string, unknown>,
  revisions: {} as Record<string, unknown>,
  detail: {} as Record<string, unknown>,
  posted: null as unknown,
  postedTo: '',
  gets: [] as string[],
}

vi.mock('../api/client', () => ({
  api: {
    // URL-aware, unlike the editor page's own mock: the drawer issues its own reads and a blind
    // getter would hand it the editor form and crash the render.
    get: (u: string) => {
      state.gets.push(u)
      if (/\/revisions\/\d+$/.test(u)) return Promise.resolve(state.detail)
      if (u.endsWith('/revisions')) return Promise.resolve(state.revisions)
      return Promise.resolve(state.form)
    },
    post: (u: string, b: unknown) => {
      state.postedTo = u
      state.posted = b
      return Promise.resolve({ ok: true, updated_at: 'token-after-restore' })
    },
    put: () => Promise.resolve({ updated_at: 'later' }),
    del: () => Promise.resolve({ ok: true }),
  },
  errText: (_e: unknown, t: (k: string) => string) => t('common.error'),
  ApiError: class ApiError extends Error {},
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o?.count ? `${k}:${o.count}` : k) }),
}))
vi.mock('../auth', () => ({ useAuth: () => ({ can: () => true }) }))
vi.mock('./Markdown', () => ({ default: ({ md }: { md?: string }) => <div data-testid="doc">{md}</div> }))
vi.mock('../lib/datetime', () => ({
  formatReportDateTime: (s: string) => s,
  isInstant: () => true,
}))

const editorForm = {
  from: 12,
  manual: true,
  id: 12,
  updated_at: 'token-in-editor',
  symbol: '600519',
  date: '2026-09-02',
  subtype: '深度分析',
  title: '手工补充',
  body_md: '当前正文',
  audience: 'all',
  viewers: [],
  subtypes: ['深度分析'],
  groups: [],
  users: [],
  usersTruncated: false,
  today: '2026-09-02',
}

function renderEditor() {
  return render(
    <AntdApp>
      <MemoryRouter initialEntries={['/report/12/edit']}>
        <Routes>
          <Route path="/report/new" element={<ReportEditorPage />} />
          <Route path="/report/:id/edit" element={<ReportEditorPage />} />
        </Routes>
      </MemoryRouter>
    </AntdApp>,
  )
}

beforeEach(() => {
  state.form = { ...editorForm }
  state.revisions = {
    revisions: [
      { id: 7, savedAt: '2026-09-01T10:00:00Z', author: 'alice', title: '手工补充', bytes: 12 },
      { id: 6, savedAt: '2026-08-31T10:00:00Z', author: 'bob', title: '旧标题', bytes: 9 },
    ],
    current: { savedAt: '2026-09-02T10:00:00Z', author: 'carol', title: '手工补充' },
    keep: 0,
  }
  state.detail = {
    revision: { id: 7, savedAt: '2026-09-01T10:00:00Z', author: 'alice', title: '手工补充', bytes: 12, body_md: '旧正文' },
    sections: [{ heading: '一', level: 1, status: 'changed', lines: [{ op: '-', text: '旧' }, { op: '+', text: '新' }] }],
    changed: 1,
  }
  state.posted = null
  state.postedTo = ''
  state.gets = []
})

describe('the history drawer', () => {
  it('fetches nothing until it is opened', async () => {
    renderEditor()
    await screen.findByText('reportEditor.titleEdit')
    // The editor's own load, and nothing else: a drawer that fetched on mount would cost every
    // author a request for a list most of them never open.
    expect(state.gets.some((u) => u.includes('/revisions'))).toBe(false)

    await userEvent.click(screen.getByText('reportHistory.open'))
    await waitFor(() => expect(state.gets.some((u) => u.endsWith('/revisions'))).toBe(true))
  })

  it('lists the superseded versions and not the current one', async () => {
    renderEditor()
    await screen.findByText('reportEditor.titleEdit')
    await userEvent.click(screen.getByText('reportHistory.open'))

    expect(await screen.findByText('alice')).toBeTruthy()
    expect(screen.getByText('bob')).toBeTruthy()
    // The current version is stated as context above the list, not as an entry in it — it is the
    // report, not history.
    expect(screen.getByText('reportHistory.currentLine')).toBeTruthy()
  })

  it('leaves the unsaved draft alone', async () => {
    renderEditor()
    await screen.findByText('reportEditor.titleEdit')
    const body = screen.getByLabelText('reportEditor.body') as HTMLTextAreaElement
    await userEvent.clear(body)
    await userEvent.type(body, '还没保存的草稿')

    await userEvent.click(screen.getByText('reportHistory.open'))
    await screen.findByText('alice')
    await userEvent.click(screen.getByText('alice'))
    // Previewing an old version renders it into the drawer's own surface. The draft lives only in
    // React state and has no other copy, so writing it back would destroy it with no undo.
    expect((screen.getByLabelText('reportEditor.body') as HTMLTextAreaElement).value).toBe('还没保存的草稿')
  })

  it('restores against the token the editor is holding, and takes the new one back', async () => {
    renderEditor()
    await screen.findByText('reportEditor.titleEdit')
    await userEvent.click(screen.getByText('reportHistory.open'))
    await screen.findByText('alice')

    await userEvent.click(screen.getAllByText('reportHistory.restore')[0])
    // antd's Popconfirm renders its OK button with the default confirm label.
    await userEvent.click(await screen.findByText('OK'))

    await waitFor(() => expect(state.postedTo).toBe('/api/reports/12/revisions/7/restore'))
    // The editor's token, not the report's current sent_at: a restore computed from a list drawn
    // before somebody else saved has to be refused, and only the caller's token can express that.
    expect(state.posted).toEqual({ updated_at: 'token-in-editor' })

    // And the fresh token is adopted, so the next ordinary save does not conflict with the restore.
    await waitFor(() => expect(state.gets.filter((u) => u.includes('/reports/editor')).length).toBe(2))
  })
})
