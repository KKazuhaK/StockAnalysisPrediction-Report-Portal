import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ApiError } from './api/client'
import { UNCHANGED, forgetTags, getIfChanged } from './lib/conditionalGet'
import { startVisiblePoll } from './lib/visiblePoll'
import type { Announcement, AnnouncementLevel, AnnouncementScope } from './api/types'

const URL = '/api/announcements'
const LEVELS: AnnouncementLevel[] = ['notice', 'success', 'warning', 'error']

// The announcement feed, kept live for the signed-in reader.
//
// It is a separate provider from SiteProvider, and it has to be: SiteProvider is mounted ABOVE
// AuthProvider so the login page can read the brand before anybody signs in, which means it cannot
// call useAuth. Moving it down would take the title and logo away from /login. So the feed —
// which is per-reader, and is fetched with the session cookie — gets its own provider inside
// <Protected>, and AppRoutes gives it key={user} so signing in as somebody else builds a new one
// rather than reusing state belonging to the previous account.

interface AnnouncementsCtx {
  items: Announcement[]
  refresh: () => Promise<void>
}

const Ctx = createContext<AnnouncementsCtx>({ items: [], refresh: async () => {} })

// Defensive, like site.tsx's normalizeSettings and for the same reason: this renders inside the
// app shell, so a malformed payload must degrade to "no banner", never to an exception that takes
// the whole page down.
function normalize(raw: unknown): Announcement[] {
  const items = (raw as { items?: unknown })?.items
  if (!Array.isArray(items)) return []
  const out: Announcement[] = []
  for (const v of items) {
    const a = v as Partial<Announcement>
    if (typeof a?.id !== 'number') continue
    const level = String(a.level ?? '').toLowerCase()
    const scope = String(a.scope ?? '').toLowerCase()
    out.push({
      id: a.id,
      level: (LEVELS.includes(level as AnnouncementLevel) ? level : 'notice') as AnnouncementLevel,
      title: String(a.title ?? '').trim(),
      content: String(a.content ?? '').trim(),
      popup: a.popup === true,
      dismissible: a.dismissible === true,
      scope: (scope === 'app' ? 'app' : 'home') as AnnouncementScope,
      endsAt: String(a.endsAt ?? '').trim(),
    })
  }
  return out
}

/**
 * unexpired drops rows whose window has closed since the payload arrived.
 *
 * The server already filters by window, but startVisiblePoll stops polling in a hidden tab: a tab
 * left open behind others would otherwise keep painting an incident banner for hours after it was
 * scheduled to come down. This is why the reader payload carries endsAt at all.
 */
function unexpired(items: Announcement[], now: number): Announcement[] {
  return items.filter((a) => {
    if (!a.endsAt) return true
    const end = Date.parse(a.endsAt)
    return Number.isNaN(end) || end > now
  })
}

export function AnnouncementsProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Announcement[]>([])
  const [tick, setTick] = useState(0)
  const held = useRef(false)

  const load = useCallback(async () => {
    // Forget the tag BEFORE asking, not after a 304 comes back. getIfChanged's tag map is keyed by
    // URL alone and lives for the lifetime of the module, so on a shared machine — sign out, sign
    // in as someone else, same tab, no reload — a fresh provider would send the previous account's
    // ETag, the server would compute the same bytes for a different reader, and 304 would mean
    // "keep what you have" to a component that has nothing. That reader would see no
    // announcements at all, and every poll for the next hour would re-confirm it.
    if (!held.current) forgetTags(URL)
    try {
      const res = await getIfChanged<{ items: unknown }>(URL)
      held.current = true
      if (res !== UNCHANGED) setItems(normalize(res))
    } catch (e) {
      // A dead session must not leave a targeted announcement on the screen. Anything else — a
      // blip, a 500 — keeps the last good payload: blanking a live incident banner because one
      // poll failed is the wrong direction to fail in.
      if (e instanceof ApiError && e.status === 401) {
        held.current = false
        setItems([])
      }
    }
  }, [])

  useEffect(() => {
    void load()
    return startVisiblePoll(() => load(), 60000, { skipLeading: true })
  }, [load])

  // Re-evaluate the display window on the same cadence as the poll, so a banner with an end time
  // retires itself even in a tab that has stopped asking the server anything. Only when something
  // actually has an end time: this provider wraps the whole signed-in app, so an unconditional
  // interval would re-render all of it every minute for a clock nobody is reading.
  const watchesClock = items.some((a) => !!a.endsAt)
  useEffect(() => {
    if (!watchesClock) return
    const timer = window.setInterval(() => setTick((n) => n + 1), 60000)
    return () => window.clearInterval(timer)
  }, [watchesClock])

  const value = useMemo<AnnouncementsCtx>(
    // eslint-disable-next-line react-hooks/exhaustive-deps -- tick is the clock, not data
    () => ({ items: unexpired(items, Date.now()), refresh: load }),
    [items, load, tick],
  )
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useAnnouncements(): AnnouncementsCtx {
  return useContext(Ctx)
}

/** Which announcements belong on the route currently rendered. */
export function inScope(items: Announcement[], pathname: string): Announcement[] {
  const onHome = pathname === '/'
  return items.filter((a) => (a.scope === 'app' ? true : onHome))
}
