import { describe, it, expect, vi, beforeEach } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import SiteAnnouncement, { announcementAlertType } from './SiteAnnouncement'
import type { SiteSettings } from '../api/types'

const siteState = vi.hoisted(() => ({
  settings: {} as SiteSettings,
}))

vi.mock('../site', () => ({
  useSite: () => ({ settings: siteState.settings }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

const POPUP_DISMISSED_KEY = 'report-portal.site-announcement.popup.dismissed'

const baseSettings: SiteSettings = {
  siteTitle: '',
  siteLogoUrl: '',
  footerText: '',
  footerShowInfo: true,
  footerShowVersion: true,
  pwaEnabled: true,
  pwaIconUrl: '',
  announcementEnabled: true,
  announcementPopup: true,
  announcementLevel: 'warning',
  announcementTitle: '维护通知',
  announcementContent: '今晚 22:00 开始维护。',
}

function renderAnnouncement(path = '/') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <SiteAnnouncement />
    </MemoryRouter>,
  )
}

describe('announcementAlertType', () => {
  it('maps announcement levels to Ant Design alert types', () => {
    expect(announcementAlertType('notice')).toBe('info')
    expect(announcementAlertType('success')).toBe('success')
    expect(announcementAlertType('warning')).toBe('warning')
    expect(announcementAlertType('error')).toBe('error')
    expect(announcementAlertType('unknown')).toBe('info')
  })
})

describe('SiteAnnouncement', () => {
  beforeEach(() => {
    window.localStorage.clear()
    siteState.settings = { ...baseSettings }
  })

  it('does not render when the announcement is disabled or empty', () => {
    siteState.settings = { ...baseSettings, announcementEnabled: false }
    const { container, rerender } = render(
      <MemoryRouter>
        <SiteAnnouncement />
      </MemoryRouter>,
    )
    expect(container.querySelector('.rp-announcement')).toBeNull()

    siteState.settings = { ...baseSettings, announcementTitle: ' ', announcementContent: '' }
    rerender(
      <MemoryRouter>
        <SiteAnnouncement />
      </MemoryRouter>,
    )
    expect(container.querySelector('.rp-announcement')).toBeNull()
  })

  it('renders the persistent banner and opens the popup on Home', async () => {
    renderAnnouncement('/')

    expect(await screen.findAllByText('维护通知')).toHaveLength(2)
    expect(screen.getAllByText('今晚 22:00 开始维护。')).toHaveLength(2)
    expect(screen.getByText('announcement.dontShowAgain')).toBeTruthy()
    expect(document.querySelector('.rp-announcement')).not.toBeNull()
  })

  it('keeps the banner but skips the popup away from Home', async () => {
    renderAnnouncement('/login')

    expect(await screen.findByText('维护通知')).toBeTruthy()
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
  })

  it('stores popup dismissal for the current announcement and reopens when content changes', async () => {
    const user = userEvent.setup()
    renderAnnouncement('/')

    await user.click(await screen.findByText('announcement.dontShowAgain'))
    expect(window.localStorage.getItem(POPUP_DISMISSED_KEY)).toBeTruthy()

    cleanup()
    renderAnnouncement('/')
    await waitFor(() => expect(screen.queryByText('announcement.dontShowAgain')).toBeNull())

    cleanup()
    siteState.settings = { ...baseSettings, announcementContent: '新的维护窗口。' }
    renderAnnouncement('/')
    expect(await screen.findByText('announcement.dontShowAgain')).toBeTruthy()
  })
})
