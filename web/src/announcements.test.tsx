import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { AnnouncementsProvider, inScope, useAnnouncements } from './announcements'
import { ApiError } from './api/client'
import type { Announcement } from './api/types'

const mocks = vi.hoisted(() => ({ getIfChanged: vi.fn(), forgetTags: vi.fn(), UNCHANGED: Symbol('unchanged') }))

vi.mock('./lib/conditionalGet', () => ({
  getIfChanged: mocks.getIfChanged,
  forgetTags: mocks.forgetTags,
  UNCHANGED: mocks.UNCHANGED,
}))

function Probe() {
  const { items } = useAnnouncements()
  return <div data-testid="titles">{items.map((a) => a.title).join('|')}</div>
}

const titles = () => screen.getByTestId('titles').textContent

function renderProvider() {
  return render(
    <AnnouncementsProvider>
      <Probe />
    </AnnouncementsProvider>,
  )
}

describe('AnnouncementsProvider', () => {
  beforeEach(() => {
    mocks.getIfChanged.mockReset()
    mocks.forgetTags.mockReset()
  })
  afterEach(() => vi.useRealTimers())

  it('loads the feed and normalizes a hostile payload instead of throwing in the shell', async () => {
    mocks.getIfChanged.mockResolvedValue({
      items: [
        { id: 1, title: ' 维护 ', level: 'CRITICAL', scope: 'everywhere', popup: 'yes' },
        { title: 'no id' }, // dropped
        null,
      ],
    })
    renderProvider()
    await waitFor(() => expect(titles()).toBe('维护'))
  })

  it('tolerates a payload that is not a list at all', async () => {
    mocks.getIfChanged.mockResolvedValue({ items: 'nope' })
    renderProvider()
    await waitFor(() => expect(mocks.getIfChanged).toHaveBeenCalled())
    expect(titles()).toBe('')
  })

  // getIfChanged's tag map is module-level and keyed by URL alone, so it outlives the session that
  // earned it. A provider mounting with nothing must not be told "nothing changed".
  it('forgets the stored ETag before its first request, not after a 304', async () => {
    mocks.getIfChanged.mockResolvedValue({ items: [] })
    renderProvider()
    await waitFor(() => expect(mocks.getIfChanged).toHaveBeenCalled())
    expect(mocks.forgetTags).toHaveBeenCalledWith('/api/announcements')
    expect(mocks.forgetTags.mock.invocationCallOrder[0])
      .toBeLessThan(mocks.getIfChanged.mock.invocationCallOrder[0])
  })

  it('keeps the last good payload through a transient failure', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mocks.getIfChanged
      .mockResolvedValueOnce({ items: [{ id: 1, title: '故障中' }] })
      .mockRejectedValue(new ApiError(500, 'boom'))
    renderProvider()
    await waitFor(() => expect(titles()).toBe('故障中'))

    await vi.advanceTimersByTimeAsync(61_000)
    await waitFor(() => expect(mocks.getIfChanged).toHaveBeenCalledTimes(2))
    // Blanking a live incident banner because one poll failed is the wrong way to fail.
    expect(titles()).toBe('故障中')
  })

  it('clears a targeted announcement the moment the session is gone', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mocks.getIfChanged
      .mockResolvedValueOnce({ items: [{ id: 1, title: '仅限华东' }] })
      .mockRejectedValue(new ApiError(401, 'unauthorized', 'session_expired'))
    renderProvider()
    await waitFor(() => expect(titles()).toBe('仅限华东'))

    await vi.advanceTimersByTimeAsync(61_000)
    await waitFor(() => expect(titles()).toBe(''))
  })

  // startVisiblePoll stops asking in a hidden tab, so a background tab would keep painting an
  // expired incident banner. This is why the reader payload carries endsAt.
  it('retires an expired announcement without waiting for the next poll', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const soon = new Date(Date.now() + 30_000).toISOString()
    mocks.getIfChanged.mockResolvedValue({ items: [{ id: 1, title: '就要结束了', endsAt: soon }] })
    renderProvider()
    await waitFor(() => expect(titles()).toBe('就要结束了'))

    await vi.advanceTimersByTimeAsync(61_000)
    await waitFor(() => expect(titles()).toBe(''))
  })
})

describe('inScope', () => {
  const a = (over: Partial<Announcement>): Announcement => ({
    id: 1, level: 'notice', title: '', content: '', popup: false,
    dismissible: false, scope: 'home', endsAt: '', ...over,
  })

  it('keeps a home announcement on Home and lets an app one follow the reader', () => {
    const home = a({ id: 1, scope: 'home' })
    const app = a({ id: 2, scope: 'app' })
    expect(inScope([home, app], '/').map((x) => x.id)).toEqual([1, 2])
    expect(inScope([home, app], '/queue').map((x) => x.id)).toEqual([2])
  })
})
