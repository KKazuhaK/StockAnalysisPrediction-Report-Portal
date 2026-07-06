import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SiteProvider, useSite } from './site'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('./api/client', () => ({
  api: apiMock,
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

function SiteProbe() {
  const site = useSite()
  return (
    <div>
      <div data-testid="title">{site.title}</div>
      <div data-testid="logo">{site.logoUrl}</div>
      <pre data-testid="settings">{JSON.stringify(site.settings)}</pre>
      <button onClick={() => site.refresh()}>refresh</button>
    </div>
  )
}

function settings() {
  return JSON.parse(screen.getByTestId('settings').textContent || '{}')
}

describe('SiteProvider', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    document.title = ''
    document.querySelector('link[rel="icon"]')?.remove()
  })

  it('normalizes public site settings from the backend', async () => {
    apiMock.get.mockResolvedValue({
      siteTitle: ' 智研平台 ',
      siteLogoUrl: ' /brand/logo.png ',
      footerText: ' <strong>备案</strong> ',
      footerShowInfo: false,
      footerShowVersion: false,
      pwaEnabled: false,
      pwaIconUrl: ' /brand/app.png ',
      announcementEnabled: true,
      announcementPopup: true,
      announcementLevel: ' CRITICAL ',
      announcementTitle: ' 维护通知 ',
      announcementContent: ' 今晚维护 ',
    })

    render(
      <SiteProvider>
        <SiteProbe />
      </SiteProvider>,
    )

    await waitFor(() => expect(screen.getByTestId('title').textContent).toBe('智研平台'))
    expect(screen.getByTestId('logo').textContent).toBe('/brand/logo.png')
    expect(settings()).toMatchObject({
      siteTitle: '智研平台',
      siteLogoUrl: '/brand/logo.png',
      footerText: '<strong>备案</strong>',
      footerShowInfo: false,
      footerShowVersion: false,
      pwaEnabled: false,
      pwaIconUrl: '/brand/app.png',
      announcementEnabled: true,
      announcementPopup: true,
      announcementLevel: 'notice',
      announcementTitle: '维护通知',
      announcementContent: '今晚维护',
    })
    expect(document.title).toBe('智研平台')
    expect(document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href).toContain('/brand/logo.png')
  })

  it('refreshes settings on demand and falls back to the localized brand title when unset', async () => {
    const user = userEvent.setup()
    apiMock.get
      .mockResolvedValueOnce({ siteTitle: '', announcementLevel: 'success' })
      .mockResolvedValueOnce({ siteTitle: ' 二次刷新 ', announcementLevel: 'error' })

    render(
      <SiteProvider>
        <SiteProbe />
      </SiteProvider>,
    )

    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(1))
    expect(screen.getByTestId('title').textContent).toBe('brand')
    expect(settings().announcementLevel).toBe('success')

    await user.click(screen.getByRole('button', { name: 'refresh' }))
    await waitFor(() => expect(screen.getByTestId('title').textContent).toBe('二次刷新'))
    expect(settings().announcementLevel).toBe('error')
  })
})
