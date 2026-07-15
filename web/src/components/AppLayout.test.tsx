import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { Grid } from 'antd'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import AppLayout from './AppLayout'

const updateState = vi.hoisted(() => ({ available: false }))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../site', () => ({
  SiteLogo: () => <span data-testid="site-logo" />,
  useSite: () => ({
    title: 'Report Portal',
    settings: { footerText: '', footerShowInfo: false, footerShowVersion: false },
  }),
}))
vi.mock('../prefs', () => ({
  usePrefs: () => ({ mode: 'light', setMode: vi.fn(), lang: 'en', setLang: vi.fn(), langs: [{ code: 'en', label: 'English' }] }),
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ wide: false }) }))
vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', name: 'Alice', admin: true, can: () => true, logout: vi.fn() }),
}))
vi.mock('../api/client', () => ({ api: { get: () => Promise.resolve({}) } }))
vi.mock('../lib/useVersionCheck', () => ({ useVersionCheck: () => updateState.available }))
vi.mock('./Omnibox', () => ({ default: () => <input aria-label="global-search" /> }))
vi.mock('./RunAnalysisModal', () => ({ default: () => null }))
vi.mock('./QueueDrawer', () => ({ default: () => null }))
vi.mock('./SiteAnnouncement', () => ({ default: () => null }))

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<AppLayout />}>
          <Route path="chat" element={<div>chat-body</div>} />
          <Route path="queue" element={<div>queue-body</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

describe('AppLayout mobile chat focus mode', () => {
  beforeEach(() => {
    updateState.available = false
  })

  it('removes global search, actions, breadcrumbs, and content gutters on mobile chat', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: false } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/chat')

    expect(await screen.findByText('chat-body')).toBeTruthy()
    const header = container.querySelector<HTMLElement>('.rp-app-header--chat-focus')
    expect(header).not.toBeNull()
    expect(header?.style.display).toBe('none')
    expect(container.querySelector('.rp-chat-content--mobile')).not.toBeNull()
    expect(screen.queryByLabelText('global-search')).toBeNull()
    expect(screen.queryByTitle('nav.runAnalysis')).toBeNull()
    expect(screen.queryByText('nav.home')).toBeNull()
  })

  it('keeps normal mobile portal chrome away from chat', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: false } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(container.querySelector('.rp-app-header--chat-focus')).toBeNull()
    expect(screen.getByLabelText('global-search')).toBeTruthy()
    expect(screen.getByTitle('nav.runAnalysis')).toBeTruthy()
    expect(screen.getByText('nav.home')).toBeTruthy()
  })

  it('uses the info background without a dark separator under the update banner', async () => {
    updateState.available = true
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/queue')

    expect(await screen.findByText('update.desc')).toBeTruthy()
    const banner = container.querySelector<HTMLElement>('.rp-update-banner')
    expect(banner).not.toBeNull()
    expect(banner?.style.borderBottom).toBe('')
    expect(screen.queryByLabelText('common.cancel')).toBeNull()
  })
})
