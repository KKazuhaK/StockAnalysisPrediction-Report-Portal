import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Grid } from 'antd'
import { MemoryRouter, Route, Routes } from 'react-router'
import AppLayout from './AppLayout'

const updateState = vi.hoisted(() => ({ available: false }))
// The header's queue badge polls through the conditional-GET helper; `queue` decides whether it has
// a count yet. UNCHANGED is what a 304 looks like — an answer to a tag this mount never sent.
const queueState = vi.hoisted(() => ({ answer: null as unknown }))
const UNCHANGED = vi.hoisted(() => Symbol('unchanged'))
const forgetTags = vi.hoisted(() => vi.fn())
const siteState = vi.hoisted(() => ({
  settings: { footerText: '', footerShowInfo: false, footerShowVersion: false },
}))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../site', () => ({
  SiteLogo: () => <span data-testid="site-logo" />,
  useSite: () => ({
    title: 'Report Portal',
    settings: siteState.settings,
  }),
}))
vi.mock('../prefs', () => ({
  usePrefs: () => ({ mode: 'light', setMode: vi.fn(), lang: 'en', setLang: vi.fn(), langs: [{ code: 'en', label: 'English' }] }),
}))
vi.mock('../reader', () => ({ useReaderPrefs: () => ({ wide: false }) }))
vi.mock('../auth', () => ({
  useAuth: () => ({ user: 'alice', name: 'Alice', admin: true, can: () => true, logout: vi.fn() }),
}))
vi.mock('../api/client', () => ({
  api: { get: () => Promise.resolve({ version: 'v9.9.9', commit: 'abc1234', buildDate: '2026-08-10' }) },
}))
vi.mock('../lib/useVersionCheck', () => ({ useVersionCheck: () => updateState.available }))
vi.mock('../lib/conditionalGet', () => ({
  UNCHANGED,
  forgetTags,
  getIfChanged: () => (queueState.answer === null ? new Promise(() => {}) : Promise.resolve(queueState.answer)),
}))
vi.mock('./Omnibox', () => ({ default: () => <input aria-label="global-search" /> }))
vi.mock('./RunAnalysisModal', () => ({ default: () => null }))
vi.mock('./QueueDrawer', () => ({ default: () => null }))
vi.mock('./SiteAnnouncement', () => ({
  default: () => null,
  AnnouncementStrip: () => null,
  AnnouncementPopup: () => null,
}))

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<AppLayout />}>
          <Route path="chat" element={<div>chat-body</div>} />
          <Route path="queue" element={<div>queue-body</div>} />
          <Route path="review" element={<div>review-body</div>} />
          <Route path="apps" element={<div>apps-body</div>} />
          <Route path="manage" element={<div>manage-body</div>} />
          <Route path="report/new" element={<div>write-report-body</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  )
}

// An absent badge is how this header says "nothing is queued". It used to say that before anything
// had been asked, and — on a summary request that keeps failing — for ever.
describe('AppLayout queue badge', () => {
  beforeEach(() => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    queueState.answer = null
    forgetTags.mockClear()
  })

  // antd keeps a hidden badge in the DOM to animate it out, so "no badge" is data-show="false"
  // rather than an absent node.
  const shown = (c: HTMLElement, sel: string) => c.querySelector(`${sel}[data-show="true"]`)

  it('shows a dot rather than no badge while the count is unknown', async () => {
    const { container } = renderAt('/queue')
    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(shown(container, '.ant-badge-dot')).not.toBeNull()
    expect(shown(container, '.ant-badge-count')).toBeNull()
  })

  it('drops the badge once the server has actually said the queue is empty', async () => {
    queueState.answer = { running: 0, waiting: 0, scheduled: 0, budget: 3 }
    const { container } = renderAt('/queue')
    expect(await screen.findByText('queue-body')).toBeTruthy()
    await vi.waitFor(() => expect(shown(container, '.ant-badge-dot')).toBeNull())
    expect(shown(container, '.ant-badge-count')).toBeNull()
  })

  it('counts the runs once it has them', async () => {
    queueState.answer = { running: 2, waiting: 1, scheduled: 0, budget: 3 }
    const { container } = renderAt('/queue')
    expect(await screen.findByText('queue-body')).toBeTruthy()
    await vi.waitFor(() => expect(shown(container, '.ant-badge-count')?.textContent).toContain('3'))
    expect(shown(container, '.ant-badge-dot')).toBeNull()
  })

  // Same module-global tag store as the queue table: the badge must not be told "unchanged" about
  // a count it has never held.
  it('drops the summary tag before its first ask', async () => {
    queueState.answer = { running: 0, waiting: 0, scheduled: 0, budget: 3 }
    renderAt('/queue')
    await vi.waitFor(() => expect(forgetTags).toHaveBeenCalledWith('/api/admin/batch/queue'))
  })
})

describe('AppLayout desktop navigation', () => {
  beforeEach(() => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
  })

  it('groups secondary destinations in a single workbench launcher', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'nav.chat' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'nav.review' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'nav.apps' })).toBeNull()

    await user.click(screen.getByRole('button', { name: 'nav.workbench' }))

    expect(screen.getByRole('menu', { name: 'nav.workbench' })).toBeTruthy()
    expect(screen.getByRole('menuitem', { name: 'nav.chat' })).toBeTruthy()
    expect(screen.getByRole('menuitem', { name: 'nav.review' })).toBeTruthy()
    expect(screen.getByRole('menuitem', { name: 'nav.apps' })).toBeTruthy()
    expect(screen.queryByRole('menuitem', { name: 'nav.manage' })).toBeNull()
  })

  it('navigates from the launcher and closes it', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    const trigger = await screen.findByRole('button', { name: 'nav.workbench' })
    await user.click(trigger)
    await user.click(screen.getByRole('menuitem', { name: 'nav.review' }))

    expect(await screen.findByText('review-body')).toBeTruthy()
    await vi.waitFor(() => expect(trigger.getAttribute('aria-expanded')).toBe('false'))
  })

  it('keeps management in the account menu at desktop width', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'nav.manage' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Alice' }))

    expect(screen.getByRole('button', { name: 'nav.manage' })).toBeTruthy()
  })

  it('moves write report into the run-analysis dropdown', async () => {
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('queue-body')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'nav.writeReport' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'nav.runActions' }))
    await user.click(screen.getByRole('menuitem', { name: 'nav.writeReport' }))

    expect(await screen.findByText('write-report-body')).toBeTruthy()
  })
})

describe('AppLayout mobile chat focus mode', () => {
  beforeEach(() => {
    updateState.available = false
    siteState.settings = { footerText: '', footerShowInfo: false, footerShowVersion: false }
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

  // The footer's name and version sat at different heights: the name and logo were grouped in an
  // inline-flex box inside a flex row, an inline-flex box takes its baseline from its first flex
  // item (here a replaced <img>), and centring that taller box left its text 1.25px above the
  // version's. jsdom has no layout, so what is asserted here is the arrangement that caused it —
  // one inline flow, no part boxed off in its own flex container. The 1.25px → 0 measurement
  // itself was made in a browser.
  it('lays the whole footer out in one inline flow so its parts share a baseline', async () => {
    siteState.settings = { footerText: '', footerShowInfo: true, footerShowVersion: true }
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderAt('/queue')

    const footer = await screen.findByText('v9.9.9').then((el) => el.closest('.ant-layout-footer'))
    expect(footer).not.toBeNull()
    expect(footer!.querySelector('.ant-space'), 'a flex row re-splits the baselines').toBeNull()
    const flexed = [...footer!.querySelectorAll<HTMLElement>('*')].filter((el) => el.style.display.includes('flex'))
    expect(flexed.map((el) => el.textContent)).toEqual([])
    expect(container.querySelector('[data-testid="site-logo"]')).not.toBeNull()
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
