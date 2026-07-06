import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import AnnouncementPage from './AnnouncementPage'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

const refreshMock = vi.hoisted(() => vi.fn())

vi.mock('../../api/client', () => ({
  api: apiMock,
}))

vi.mock('../../site', () => ({
  useSite: () => ({ refresh: refreshMock }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

const loadedSettings = {
  oldBase: '',
  oldUser: '',
  hasPass: false,
  timezone: 'Asia/Shanghai',
  siteTitle: 'Should stay out of the announcement save',
  siteLogoUrl: '/logo.png',
  footerText: 'footer',
  footerShowInfo: false,
  footerShowVersion: false,
  pwaEnabled: false,
  pwaIconUrl: '/app.png',
  announcementEnabled: true,
  announcementPopup: true,
  announcementLevel: 'warning',
  announcementTitle: '维护窗口',
  announcementContent: '今晚 22:00 开始维护。',
  newCount: 3,
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
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    refreshMock.mockReset()
    apiMock.get.mockResolvedValue({ ...loadedSettings })
    apiMock.post.mockResolvedValue({})
    refreshMock.mockResolvedValue({})
  })

  it('loads existing announcement settings and renders a live preview', async () => {
    renderPage()

    expect(apiMock.get).toHaveBeenCalledWith('/api/admin/settings')
    expect(await screen.findByDisplayValue('维护窗口')).toBeTruthy()
    expect(screen.getByDisplayValue('今晚 22:00 开始维护。')).toBeTruthy()
    expect(screen.getByText('维护窗口')).toBeTruthy()
    expect(document.querySelector('.rp-announcement')).not.toBeNull()
  })

  it('saves only announcement fields and refreshes site chrome', async () => {
    const user = userEvent.setup()
    renderPage()

    const title = await screen.findByPlaceholderText('settings.announcementTitlePlaceholder')
    await user.clear(title)
    await user.type(title, '节点已恢复')

    const content = screen.getByPlaceholderText('settings.announcementContentPlaceholder')
    await user.clear(content)
    await user.type(content, '监控已恢复正常。')

    const [enabledSwitch] = screen.getAllByRole('switch')
    await user.click(enabledSwitch)

    await user.click(screen.getByRole('button', { name: /common\.save/ }))

    await waitFor(() => expect(apiMock.post).toHaveBeenCalledTimes(1))
    expect(apiMock.post).toHaveBeenCalledWith('/api/admin/settings', {
      announcementEnabled: false,
      announcementPopup: true,
      announcementLevel: 'warning',
      announcementTitle: '节点已恢复',
      announcementContent: '监控已恢复正常。',
    })
    expect(Object.keys(apiMock.post.mock.calls[0][1]).sort()).toEqual([
      'announcementContent',
      'announcementEnabled',
      'announcementLevel',
      'announcementPopup',
      'announcementTitle',
    ])
    expect(refreshMock).toHaveBeenCalledTimes(1)
  })
})
