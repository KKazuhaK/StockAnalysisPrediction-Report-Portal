import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import RecurringConsole from './RecurringConsole'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}))

vi.mock('../api/client', () => ({ api: apiMock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true }) }))

const targets = { targets: [{ id: 1, plugin_slug: 'dify', name: 'Daily Review', created_at: '', inputs: [{ key: 'code' }] }] }
const tasks = {
  tasks: [
    {
      id: 7,
      name: 'Close review',
      target_id: 1,
      target_name: 'Daily Review',
      concurrency: 1,
      priority: '',
      max_retries: 2,
      freq: 'daily',
      at_time: '09:30',
      weekday: 1,
      monthday: 1,
      enabled: true,
      created_by: 'alice',
      created_at: '2026-07-13 08:00:00',
      last_fired: '2026-07-12',
      row_count: 3,
      next_run: '2026-07-14 09:30:00',
    },
  ],
}

function renderPage() {
  return render(
    <App>
      <RecurringConsole />
    </App>,
  )
}

describe('RecurringConsole', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.put.mockReset()
    apiMock.del.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/batch/targets')) return Promise.resolve({ ...targets })
      if (url === '/api/admin/batch/recurring') return Promise.resolve({ ...tasks })
      if (url === '/api/admin/batch/recurring/7')
        return Promise.resolve({ ...tasks.tasks[0], rows: [{ code: '600000' }, { code: '000001' }], history: [] })
      return Promise.resolve({})
    })
    apiMock.post.mockResolvedValue({ ok: true, job_id: 42 })
  })

  it('lists tasks with their cadence, next run, and target', async () => {
    renderPage()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/batch/recurring'))
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/batch/targets')
    expect(await screen.findByText('Close review')).toBeTruthy()
    expect(screen.getByText('recurring.cadence.daily')).toBeTruthy() // daily label key
    expect(screen.getByText('2026-07-14 09:30:00')).toBeTruthy() // next run
    expect(screen.getByText('recurring.priorityNormal')).toBeTruthy() // '' → normal tag
  })

  it('toggling the enable switch posts to /enable', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByRole('switch'))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledWith('/api/admin/batch/recurring/7/enable', { enabled: false }))
  })

  it('the new-task button opens the create modal', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByRole('button', { name: /recurring\.new/ }))
    // the modal renders the name + target fields.
    expect((await screen.findAllByText('recurring.fieldName')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('recurring.fieldTarget').length).toBeGreaterThan(0)
  })

  it('editing prefills the CSV editor WITH a header row so the template round-trips', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('Close review')
    await user.click(screen.getByLabelText('common.edit')) // the aria-labelled edit action
    await screen.findByText('recurring.edit') // modal title (editing mode)
    const ta = screen.getByPlaceholderText('batch.csvPlaceholder') as HTMLTextAreaElement
    // header line + both data rows — NOT data-only (which csvToRows would eat as a header, blanking it).
    expect(ta.value).toBe('code\n600000\n000001')
  })
})
