import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import AnnouncementPage from './AnnouncementPage'
import type { AdminAnnouncement } from '../../api/types'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  patch: vi.fn(),
  del: vi.fn(),
}))

const forgetTagsMock = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', () => ({
  api: apiMock,
  errText: (e: unknown) => String(e),
}))

vi.mock('../../lib/conditionalGet', () => ({
  forgetTags: forgetTagsMock,
  UNCHANGED: Symbol('unchanged'),
  getIfChanged: vi.fn(),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o?.count ? `${k}:${o.count}` : k) }),
}))

function announcement(over: Partial<AdminAnnouncement> = {}): AdminAnnouncement {
  return {
    id: 1,
    level: 'warning',
    title: '维护窗口',
    content: '今晚 22:00 开始维护。',
    ord: 0,
    enabled: true,
    popup: false,
    dismissible: false,
    scope: 'home',
    audience: 'all',
    grants: [],
    startsAt: '',
    endsAt: '',
    createdAt: '2026-08-01T00:00:00Z',
    createdBy: 'admin',
    updatedAt: '2026-08-01T00:00:00Z',
    ...over,
  }
}

function renderPage() {
  return render(
    <App>
      <AnnouncementPage />
    </App>,
  )
}

describe('AnnouncementPage', () => {
  beforeEach(() => {
    for (const fn of Object.values(apiMock)) fn.mockReset()
    forgetTagsMock.mockReset()
    apiMock.get.mockResolvedValue({ items: [announcement()] })
    apiMock.post.mockResolvedValue({ ok: true, id: 9 })
    apiMock.put.mockResolvedValue({ ok: true })
    apiMock.patch.mockResolvedValue({ ok: true })
    apiMock.del.mockResolvedValue({ ok: true })
  })

  it('lists the announcements from the new endpoint', async () => {
    renderPage()
    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/announcements')
    expect(await screen.findByText('维护窗口')).toBeTruthy()
    expect(screen.getByText('announcementAdmin.status.live')).toBeTruthy()
  })

  it('shows an empty state rather than a settings form when there is nothing', async () => {
    apiMock.get.mockResolvedValue({ items: [] })
    renderPage()
    expect(await screen.findByText('announcementAdmin.empty')).toBeTruthy()
  })

  it('creates an announcement from the drawer, with a live preview', async () => {
    apiMock.get.mockResolvedValue({ items: [] })
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('button', { name: /announcementAdmin\.add/ }))
    const title = await screen.findByPlaceholderText('settings.announcementTitlePlaceholder')
    await user.type(title, '节点已恢复')
    expect(document.querySelector('.rp-announcement')).not.toBeNull() // preview follows the form

    await user.click(screen.getByRole('button', { name: /common\.save/ }))
    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post.mock.calls[0][0]).toBe('/api/admin/announcements')
    expect(apiMock.post.mock.calls[0][1]).toMatchObject({ title: '节点已恢复', enabled: true, popup: false })
  })

  // Optimistic concurrency: without the token a second admin's save silently reverts the first.
  it('sends the loaded updatedAt when editing so a stale save can be refused', async () => {
    const user = userEvent.setup()
    const { container } = renderPage()
    await screen.findByText('维护窗口')

    await user.click(container.querySelector('.anticon-edit')!.closest('button')!)
    const title = await screen.findByPlaceholderText('settings.announcementTitlePlaceholder')
    await user.clear(title)
    await user.type(title, '改过的标题')
    await user.click(screen.getByRole('button', { name: /common\.save/ }))

    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(1))
    expect(apiMock.put.mock.calls[0][0]).toBe('/api/admin/announcements/1')
    expect(apiMock.put.mock.calls[0][1]).toMatchObject({
      title: '改过的标题',
      updatedAt: '2026-08-01T00:00:00Z',
    })
  })

  // A whole-row PUT here would write back the title and body this page loaded minutes ago.
  it('flips the row switch with a PATCH of that one field', async () => {
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('switch'))
    await waitFor(() => expect(apiMock.patch).toHaveBeenCalledTimes(1))
    expect(apiMock.patch).toHaveBeenCalledWith('/api/admin/announcements/1', { enabled: false })
    expect(apiMock.put).not.toHaveBeenCalled()
  })

  it('labels the popup switches that will not actually fire', async () => {
    apiMock.get.mockResolvedValue({
      items: [
        announcement({ id: 1, title: '第一条', popup: true }),
        announcement({ id: 2, title: '第二条', popup: true }),
      ],
    })
    renderPage()

    await screen.findByText('第二条')
    // Only one row carries the "will not pop up" tag: the second.
    const tags = screen.getAllByText('announcementAdmin.popupSkipped')
    expect(tags).toHaveLength(1)
  })

  it('nudges about an enabled announcement with no end date that has been up for weeks', async () => {
    const old = new Date(Date.now() - 40 * 86400000).toISOString()
    apiMock.get.mockResolvedValue({ items: [announcement({ updatedAt: old })] })
    renderPage()
    expect(await screen.findByText(/announcementAdmin\.stale:\d+/)).toBeTruthy()
  })

  it('reports a scheduled announcement as not yet started', async () => {
    const future = new Date(Date.now() + 86400000).toISOString()
    apiMock.get.mockResolvedValue({ items: [announcement({ startsAt: future })] })
    renderPage()
    expect(await screen.findByText('announcementAdmin.status.scheduled')).toBeTruthy()
  })

  it('drops the reader feed’s cached ETag after a change so the admin’s own shell updates now', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('switch'))
    await waitFor(() => expect(forgetTagsMock).toHaveBeenCalledWith('/api/announcements'))
  })

  it('deletes through the row action', async () => {
    const user = userEvent.setup()
    const { container } = renderPage()
    await screen.findByText('维护窗口')

    const del = container.querySelector('.ant-btn-dangerous') as HTMLElement
    await user.click(del)
    const confirm = await screen.findByRole('tooltip')
    await user.click(within(confirm).getByRole('button', { name: /OK|确 定|确定/ }))
    await waitFor(() => expect(apiMock.del).toHaveBeenCalledWith('/api/admin/announcements/1'))
  })
})
