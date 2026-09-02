import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App as AntdApp } from 'antd'
import { MemoryRouter, Route, Routes } from 'react-router'
import ReportEditorPage from './ReportEditorPage'
import EditReportButton from '../components/EditReportButton'

// What is worth pinning here is not the form — it is which of three things a click on "edit"
// turns into, because the author cannot tell them apart and getting it wrong is not visible until
// the save fails on the identity index (or, worse, quietly makes a second hand-written report).

const state: { form: Record<string, unknown>; posted: unknown; putTo: string } = {
  form: {},
  posted: null,
  putTo: '',
}
const perms = { can: true }

vi.mock('../api/client', () => ({
  api: {
    get: () => Promise.resolve(state.form),
    post: (_u: string, b: unknown) => {
      state.posted = b
      return Promise.resolve({ id: 99 })
    },
    put: (u: string, b: unknown) => {
      state.putTo = u
      state.posted = b
      return Promise.resolve({ updated_at: 'later' })
    },
    del: () => Promise.resolve({ ok: true }),
  },
  errText: (_e: unknown, t: (k: string) => string) => t('common.error'),
  ApiError: class ApiError extends Error {
    status: number
    code?: string
    data?: unknown
    constructor(status: number, message: string, code?: string, data?: unknown) {
      super(message)
      this.status = status
      this.code = code
      this.data = data
    }
  },
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o?.count ? `${k}:${o.count}` : k) }),
}))

vi.mock('../auth', () => ({ useAuth: () => ({ can: () => perms.can }) }))

// The real one pulls in react-markdown, KaTeX and mermaid; the preview pane's job here is only to
// show the text it was given.
vi.mock('../components/Markdown', () => ({
  default: ({ md }: { md?: string }) => <div data-testid="preview">{md}</div>,
}))

const baseForm = {
  subtypes: ['深度分析'],
  groups: [{ principal: 'g:3', name: '华东' }],
  users: [{ principal: 'u:alice', name: 'alice' }],
  usersTruncated: false,
  today: '2026-09-02',
  audience: 'grant',
  viewers: [],
}

function renderEditor(path: string) {
  return render(
    <AntdApp>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/report/new" element={<ReportEditorPage />} />
          <Route path="/report/:id/edit" element={<ReportEditorPage />} />
        </Routes>
      </MemoryRouter>
    </AntdApp>,
  )
}

beforeEach(() => {
  state.form = { ...baseForm }
  state.posted = null
  state.putTo = ''
  perms.can = true
})

describe('which of three things "edit" means', () => {
  it('seeds a NEW report from a machine one, and says so before the author types', async () => {
    state.form = { ...baseForm, from: 7, manual: false, symbol: '600519', date: '2026-09-02',
      subtype: '深度分析', title: '工作流产出', body_md: '机器正文', audience: 'grant' }
    renderEditor('/report/new?from=7')

    // The heading and the note both name what is about to happen: a second report, not a change
    // to the one being read. This is the moment to say it — after saving is too late.
    expect(await screen.findByText('reportEditor.titleFork')).toBeTruthy()
    expect(screen.getByText('reportEditor.forkNote')).toBeTruthy()
    expect((screen.getByLabelText('reportEditor.body') as HTMLTextAreaElement).value).toBe('机器正文')
  })

  it('edits in place when the report opened from is already hand-written', async () => {
    // Reached by clicking "edit" on a hand-written report: the button cannot know the version, so
    // the server says, and the editor turns the create into an edit. Without this the save collides
    // with the very report it was seeded from.
    state.form = { ...baseForm, from: 12, manual: true, id: 12, updated_at: 'tok',
      symbol: '600519', date: '2026-09-02', subtype: '深度分析', title: '手工补充', body_md: '正文' }
    renderEditor('/report/new?from=12')
    expect(await screen.findByText('reportEditor.titleEdit')).toBeTruthy()
  })

  it('opens the existing hand-written form rather than making a second one', async () => {
    state.form = { ...baseForm, from: 7, manual: false, manualId: 34, title: 't', body_md: 'x' }
    renderEditor('/report/new?from=7')
    // Redirected to /report/34/edit — which the router renders as the in-place editor.
    expect(await screen.findByText('reportEditor.titleEdit')).toBeTruthy()
  })

  it('is a blank form with no source report', async () => {
    renderEditor('/report/new')
    expect(await screen.findByText('reportEditor.titleNew')).toBeTruthy()
    expect((screen.getByLabelText('reportEditor.body') as HTMLTextAreaElement).value).toBe('')
  })
})

describe('saving', () => {
  it('sends the body, the date and the audience the form was filled with', async () => {
    state.form = { ...baseForm, from: 12, manual: true, id: 12, updated_at: 'tok',
      symbol: '600519', date: '2026-09-02', subtype: '深度分析', title: '手工补充',
      body_md: '正文', audience: 'all', viewers: [] }
    renderEditor('/report/12/edit')
    await screen.findByText('reportEditor.titleEdit')

    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(state.putTo).toBe('/api/reports/12'))
    const sent = state.posted as Record<string, unknown>
    expect(sent.body_md).toBe('正文')
    expect(sent.date).toBe('2026-09-02')
    expect(sent.audience).toBe('all')
    // Everyone means everyone: an audience list left over from a previous choice must not ride
    // along, or the server would store recipients for a report addressed to all of them.
    expect(sent.viewers).toEqual([])
    // The concurrency token loaded with the form goes back with the save.
    expect(sent.updated_at).toBe('tok')
  })

  it('sends the token the last save returned, not the one the page loaded with', async () => {
    // Otherwise the second save of one sitting always conflicts with the first — with itself.
    state.form = { ...baseForm, from: 12, manual: true, id: 12, updated_at: 'tok',
      symbol: '600519', date: '2026-09-02', subtype: '深度分析', title: 't',
      body_md: '正文', audience: 'all' }
    renderEditor('/report/12/edit')
    await screen.findByText('reportEditor.titleEdit')

    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect((state.posted as Record<string, unknown>).updated_at).toBe('tok'))
    await userEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect((state.posted as Record<string, unknown>).updated_at).toBe('later'))
  })
})

describe('the edit entry point', () => {
  it('is absent without the permission', () => {
    perms.can = false
    const { container } = render(
      <MemoryRouter>
        <EditReportButton reportId={5} />
      </MemoryRouter>,
    )
    expect(container.textContent).toBe('')
  })

  it('always hands the report to the editor as ?from, whatever kind it is', async () => {
    render(
      <MemoryRouter initialEntries={['/stock/x']}>
        <Routes>
          <Route path="/stock/x" element={<EditReportButton reportId={5} />} />
          <Route path="/report/new" element={<div>editor</div>} />
        </Routes>
      </MemoryRouter>,
    )
    await userEvent.click(screen.getByText('reportEditor.edit'))
    // Which of the three it becomes is the server's answer, not the reading page's guess — the
    // page would otherwise need the report's version and whether a hand-written form exists.
    expect(await screen.findByText('editor')).toBeTruthy()
  })
})
