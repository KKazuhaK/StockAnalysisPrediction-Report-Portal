import { describe, it, expect, vi, beforeEach } from 'vitest'
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import SiteAnnouncement, { AnnouncementPopup, AnnouncementStrip, announcementAlertType } from './SiteAnnouncement'
import { announcementSig, migrateLegacyDismissal } from '../lib/announcementDismissal'
import type { Announcement } from '../api/types'

const feed = vi.hoisted(() => ({ items: [] as Announcement[], user: 'alice' as string | null }))

vi.mock('../announcements', async () => {
  const actual = await vi.importActual<typeof import('../announcements')>('../announcements')
  return { ...actual, useAnnouncements: () => ({ items: feed.items, refresh: async () => {} }) }
})

vi.mock('../auth', () => ({
  useAuth: () => ({ user: feed.user }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o?.count ? `${k}:${o.count}` : k) }),
}))

const LEGACY_KEY = 'report-portal.site-announcement.popup.dismissed'
const MAP_KEY = 'report-portal.announce.dismissed.v1.alice'

function announcement(over: Partial<Announcement> = {}): Announcement {
  return {
    id: 1,
    level: 'warning',
    title: '维护通知',
    content: '今晚 22:00 开始维护。',
    popup: false,
    dismissible: false,
    scope: 'home',
    endsAt: '',
    ...over,
  }
}

// The three surfaces AppLayout mounts together: the app-scoped strip on every page, the home-page
// alert stack, and the single popup over both.
function renderAt(path = '/') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <AnnouncementStrip />
      <SiteAnnouncement />
      <AnnouncementPopup />
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
    window.sessionStorage.clear()
    feed.items = []
    feed.user = 'alice'
  })

  it('renders nothing when the feed is empty', () => {
    const { container } = renderAt('/')
    expect(container.querySelector('.rp-announcement')).toBeNull()
  })

  it('stacks every announcement in the operator’s order', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条' }),
      announcement({ id: 2, title: '第二条' }),
    ]
    renderAt('/')
    const banners = await screen.findAllByText(/第.条/)
    expect(banners.map((n) => n.textContent)).toEqual(['第一条', '第二条'])
  })

  it('folds the overflow behind a counter instead of burying the page in banners', async () => {
    feed.items = [1, 2, 3, 4, 5].map((id) => announcement({ id, title: `第 ${id} 条` }))
    const user = userEvent.setup()
    renderAt('/')

    expect(await screen.findByText('第 3 条')).toBeTruthy()
    expect(screen.queryByText('第 4 条')).toBeNull()
    await user.click(screen.getByText('announcement.showMore:2'))
    expect(await screen.findByText('第 5 条')).toBeTruthy()
  })

  it('honours scope: a home announcement stays on Home, an app one follows the reader', async () => {
    feed.items = [
      announcement({ id: 1, title: '首页公告', scope: 'home' }),
      announcement({ id: 2, title: '全站公告', scope: 'app' }),
    ]
    const { container } = renderAt('/')
    expect(await screen.findByText('首页公告')).toBeTruthy()
    expect(screen.getByText('全站公告')).toBeTruthy()
    // Each is drawn once, by the surface that owns its scope — not twice on the home page.
    expect(container.querySelectorAll('.rp-announcement')).toHaveLength(1)
    expect(container.querySelector('.rp-announce-strip')).not.toBeNull()

    cleanup()
    renderAt('/queue')
    expect(await screen.findByText('全站公告')).toBeTruthy()
    expect(screen.queryByText('首页公告')).toBeNull()
  })

  // A band on every page is chrome, so it costs what chrome costs: one line, expanded on demand.
  it('collapses the site-wide strip to one line and expands it in place', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条', content: '正文一', scope: 'app' }),
      announcement({ id: 2, title: '第二条', content: '正文二', scope: 'app' }),
    ]
    const user = userEvent.setup()
    renderAt('/queue')

    expect(await screen.findByText('第一条')).toBeTruthy()
    expect(screen.getByText('+1')).toBeTruthy()
    expect(screen.queryByText('正文二')).toBeNull()

    await user.click(screen.getByRole('button', { expanded: false }))
    expect(await screen.findByText('正文二')).toBeTruthy()
  })

  it('closes a dismissible site-wide strip', async () => {
    feed.items = [announcement({ id: 1, title: '可关闭', scope: 'app', dismissible: true })]
    const user = userEvent.setup()
    const { container } = renderAt('/queue')
    await screen.findByText('可关闭')

    await user.click(screen.getByRole('button', { name: 'announcement.close' }))
    await waitFor(() => expect(container.querySelector('.rp-announce-strip')).toBeNull())
  })

  // One modal per page load. Advancing to the next would make the reader dismiss two in one
  // interaction and would contradict the hint shown beside the switch on the admin page.
  it('offers only the first eligible popup', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条', popup: true }),
      announcement({ id: 2, title: '第二条', popup: true }),
    ]
    renderAt('/')
    await screen.findByText('announcement.dontShowAgain')
    // The first announcement's title appears twice (banner + modal); the second only once.
    expect(screen.getAllByText('第一条')).toHaveLength(2)
    expect(screen.getAllByText('第二条')).toHaveLength(1)
  })

  it('remembers "don\'t show again" per announcement, not for the whole feed', async () => {
    feed.items = [
      announcement({ id: 1, title: '第一条', popup: true }),
      announcement({ id: 2, title: '第二条', popup: true }),
    ]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    // Reload: the first is silenced, so the second gets its turn — the dismissal is not global.
    cleanup()
    renderAt('/')
    await waitFor(() => expect(screen.getAllByText('第二条')).toHaveLength(2))
  })

  // The reader keys dismissal on (id, wording). Reordering changes neither, so a drag in the admin
  // console must not re-interrupt everyone who already dismissed the popup.
  it('does not re-fire a dismissed popup when the announcements are reordered', async () => {
    const a = announcement({ id: 1, title: '第一条', popup: true })
    const b = announcement({ id: 2, title: '第二条' })
    feed.items = [a, b]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    cleanup()
    feed.items = [b, a] // dragged into a new order; same rows
    renderAt('/')
    await screen.findByText('第一条')
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
  })

  it('re-fires when the operator edits what the announcement says', async () => {
    feed.items = [announcement({ id: 1, popup: true })]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    cleanup()
    feed.items = [announcement({ id: 1, popup: true, content: '维护推迟到明晚。' })]
    renderAt('/')
    expect(await screen.findByText('announcement.dontShowAgain')).toBeTruthy()
  })

  it('"got it" silences the popup for the session only, and never the banner', async () => {
    feed.items = [announcement({ id: 1, popup: true })]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.gotIt'))

    cleanup()
    renderAt('/')
    // Banner still there; popup not offered again in this session.
    expect(await screen.findByText('维护通知')).toBeTruthy()
    expect(screen.queryByText('announcement.dontShowAgain')).toBeNull()
    expect(window.localStorage.getItem(MAP_KEY)).toBeNull()
  })

  it('closes only the banner it was asked to close', async () => {
    feed.items = [
      announcement({ id: 1, title: '可关闭', dismissible: true }),
      announcement({ id: 2, title: '不可关闭' }),
    ]
    const user = userEvent.setup()
    const { container } = renderAt('/')
    await screen.findByText('可关闭')
    const close = container.querySelector('.ant-alert-close-icon') as HTMLElement
    expect(close).toBeTruthy()
    await user.click(close)
    await waitFor(() => expect(screen.queryByText('可关闭')).toBeNull())
    expect(screen.getByText('不可关闭')).toBeTruthy()
  })

  // The old key held a bare hash with no id beside it. The imported row keeps the level, title and
  // body it was taken over, so matching it back is exact — which is why the signature is computed
  // in the browser rather than on the server. It runs from the provider, over the WHOLE feed: the
  // three surfaces below each see only their own slice, and one of them consuming the key against a
  // partial list is how every reader gets re-interrupted on upgrade day.
  it('carries the pre-upgrade dismissal across, once', async () => {
    const a = announcement({ id: 7, popup: true })
    window.localStorage.setItem(LEGACY_KEY, announcementSig(a))
    feed.items = [a]
    migrateLegacyDismissal('alice', feed.items)

    renderAt('/')
    await screen.findAllByText('维护通知')
    await waitFor(() => expect(screen.queryByText('announcement.dontShowAgain')).toBeNull())
    expect(window.localStorage.getItem(LEGACY_KEY)).toBeNull()
  })

  it('keeps one reader’s dismissals off another reader’s screen', async () => {
    feed.items = [announcement({ id: 1, popup: true })]
    const user = userEvent.setup()
    renderAt('/')
    await user.click(await screen.findByText('announcement.dontShowAgain'))

    cleanup()
    feed.user = 'bob' // same browser, different account
    renderAt('/')
    expect(await screen.findByText('announcement.dontShowAgain')).toBeTruthy()
  })
})
