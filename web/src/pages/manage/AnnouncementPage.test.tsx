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

// The picker payload. The Default OU is absent because the server leaves it out: it is on every
// account's chain, so offering it would be offering "everyone" under a name that reads like a
// subset — and the save path refuses it anyway.
const GROUPS = [
  { principal: 'g:7', name: '华东' },
  { principal: 'g:8', name: '外部客户 A', restricted: true },
]
const USERS = [{ principal: 'u:alice', name: 'alice', display: '张三' }]

// antd puts pointer-events:none on a Select's placeholder, so clicking the visible text does
// nothing; in antd 6 the thing that opens the dropdown is .ant-select-input (there is no longer a
// .ant-select-selector). Find the Select by the placeholder it renders, then click its input.
async function openSelect(user: ReturnType<typeof userEvent.setup>, placeholder: string) {
  const box = screen.getByText(placeholder).closest('.ant-select') as HTMLElement
  await user.click(box.querySelector('.ant-select-input') as HTMLElement)
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
    apiMock.get.mockResolvedValue({ items: [announcement()], groups: GROUPS, users: USERS })
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

    await user.click(await screen.findByRole('switch', { name: 'announcementAdmin.enabled' }))
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
    await user.click(await screen.findByRole('switch', { name: 'announcementAdmin.enabled' }))
    await waitFor(() => expect(forgetTagsMock).toHaveBeenCalledWith('/api/announcements'))
  })

  it('names the audience on a targeted row', async () => {
    apiMock.get.mockResolvedValue({
      items: [announcement({ audience: 'grant', grants: ['g:7'] })],
      groups: GROUPS,
      users: USERS,
    })
    renderPage()
    // The group's NAME, not the principal string an operator has never seen.
    expect(await screen.findByText('华东')).toBeTruthy()
  })

  it('reveals the recipient picker only for a targeted announcement, and sends what was picked', async () => {
    apiMock.get.mockResolvedValue({ items: [], groups: GROUPS, users: USERS })
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('button', { name: /announcementAdmin\.add/ }))
    await screen.findByPlaceholderText('settings.announcementTitlePlaceholder')
    expect(screen.queryByText('announcementAdmin.grants')).toBeNull()

    await user.click(screen.getByText('announcementAdmin.audience.grant'))
    await openSelect(user, 'announcementAdmin.grantsPlaceholder')
    await user.click(await screen.findByTitle('华东'))
    await user.click(screen.getByRole('button', { name: /common\.save/ }))

    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post.mock.calls[0][1]).toMatchObject({ audience: 'grant', grants: ['g:7'] })
  })

  it('clears the recipients when an announcement goes back to everyone', async () => {
    apiMock.get.mockResolvedValue({
      items: [announcement({ audience: 'grant', grants: ['g:7'] })],
      groups: GROUPS,
      users: USERS,
    })
    const user = userEvent.setup()
    const { container } = renderPage()
    await screen.findByText('维护窗口')

    await user.click(container.querySelector('.anticon-edit')!.closest('button')!)
    await screen.findByPlaceholderText('settings.announcementTitlePlaceholder')
    await user.click(screen.getByText('announcementAdmin.audience.all'))
    await user.click(screen.getByRole('button', { name: /common\.save/ }))

    await waitFor(() => expect(apiMock.put).toHaveBeenCalledTimes(1))
    expect(apiMock.put.mock.calls[0][1]).toMatchObject({ audience: 'all', grants: [] })
  })

  // The admin does not receive their own targeted announcements, so this control is the only thing
  // standing between "addressed correctly" and "addressed to nobody".
  it('previews what a recipient would actually see', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('维护窗口')

    apiMock.get.mockResolvedValueOnce({ items: [{ id: 1, level: 'warning', title: '华东停电', content: '' }] })
    await openSelect(user, 'announcementAdmin.previewAs')
    await user.click(await screen.findByTitle('华东'))

    await waitFor(() =>
      expect(apiMock.get).toHaveBeenCalledWith('/api/admin/announcements/preview?principal=g%3A7'),
    )
    expect(await screen.findByText('华东停电')).toBeTruthy()
  })

  it('says so plainly when a recipient would see nothing', async () => {
    const user = userEvent.setup()
    renderPage()
    await screen.findByText('维护窗口')

    apiMock.get.mockResolvedValueOnce({ items: [] })
    await openSelect(user, 'announcementAdmin.previewAs')
    await user.click(await screen.findByTitle('华东'))

    expect(await screen.findByText('announcementAdmin.previewEmpty')).toBeTruthy()
  })

  // The save path refuses to CREATE a targeted announcement with no recipients, but deleting the
  // group it named produces the same state afterwards. Nothing else would say so.
  it('flags an announcement whose recipients have all gone away', async () => {
    apiMock.get.mockResolvedValue({
      items: [announcement({ audience: 'grant', grants: [] })],
      groups: GROUPS,
      users: USERS,
    })
    renderPage()
    expect(await screen.findByText('announcementAdmin.status.unreachable')).toBeTruthy()
    expect(screen.queryByText('announcementAdmin.status.live')).toBeNull()
  })

  it('does not flag one that is merely switched off', async () => {
    apiMock.get.mockResolvedValue({
      items: [announcement({ enabled: false, audience: 'grant', grants: [] })],
      groups: GROUPS,
      users: USERS,
    })
    renderPage()
    expect(await screen.findByText('announcementAdmin.status.draft')).toBeTruthy()
  })

  // Saving is never refused on count — a refusal at the moment somebody most needs to broadcast
  // does not stop readers ignoring a crowded band. The console says so instead.
  it('warns once too many announcements are live at the same time', async () => {
    const many = Array.from({ length: 6 }, (_, i) => announcement({ id: i + 1, title: `第 ${i} 条` }))
    apiMock.get.mockResolvedValue({ items: many, groups: GROUPS, users: USERS })
    renderPage()
    expect(await screen.findByText('announcementAdmin.crowded:6')).toBeTruthy()
  })

  it('counts what readers face, not the draft pile', async () => {
    const items = [
      ...Array.from({ length: 3 }, (_, i) => announcement({ id: i + 1, title: `生效 ${i}` })),
      ...Array.from({ length: 8 }, (_, i) => announcement({ id: 100 + i, title: `草稿 ${i}`, enabled: false })),
    ]
    apiMock.get.mockResolvedValue({ items, groups: GROUPS, users: USERS })
    renderPage()
    await screen.findByText('生效 0')
    expect(screen.queryByText(/announcementAdmin\.crowded/)).toBeNull()
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
