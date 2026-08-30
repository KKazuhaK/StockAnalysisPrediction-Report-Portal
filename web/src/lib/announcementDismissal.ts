// Remembering which announcements a reader has already dealt with.
//
// The old scheme was one localStorage key holding one FNV hash: it could express exactly one
// announcement, it was shared by everybody who used the browser (so one person's "don't show
// again" silenced it for the next), and JSON.parse of it throws. This replaces it, and reads the
// old key exactly once to carry the existing dismissal across.
//
// Two decisions are load-bearing:
//
// Identity is (id, signature), never position. The id is a surrogate key and ord is a separate,
// mutable column, so dragging a row cannot re-fire a popup somebody already dismissed. The
// signature is a hash of what the announcement SAYS — level, title, body — so fixing a typo does
// re-notify (the behaviour all three locales promise), while changing the audience, the display
// window, the scope, or the order does not disturb anyone. Deliberately not updated_at, which
// moves on every one of those.
//
// The signature is computed HERE rather than sent by the server, and that is what makes the
// one-time migration exact: the legacy value was produced by this same function over the same
// {level,title,content} shape, so the stored dismissal matches by construction. A Go-side hash
// would have had to reproduce JSON.stringify plus UTF-16 iteration byte for byte, and a near miss
// would have re-fired the popup for every user on upgrade day — the exact thing the migration is
// for.

import type { Announcement } from '../api/types'

const LEGACY_KEY = 'report-portal.site-announcement.popup.dismissed'
const DISMISSED_PREFIX = 'report-portal.announce.dismissed.v1.'
const SEEN_PREFIX = 'report-portal.announce.seen.'

// Entries older than this are dropped. See gc() for why age, and not "absent from the current
// payload", is the only safe criterion.
const MAX_AGE_MS = 90 * 24 * 60 * 60 * 1000
const MAX_ENTRIES = 100

/** One dismissal: the signature it was made against, and when. */
interface Dismissal {
  sig: string
  at: number
}
type DismissMap = Record<string, Dismissal>

function read(store: Storage | undefined, key: string): string {
  try {
    return store?.getItem(key) || ''
  } catch {
    return '' // private mode, or a browser configured to block site data
  }
}

function write(store: Storage | undefined, key: string, value: string) {
  try {
    store?.setItem(key, value)
  } catch {
    // Ignore: the dismissal is still honoured for this render, it just will not persist.
  }
}

function remove(store: Storage | undefined, key: string) {
  try {
    store?.removeItem(key)
  } catch {
    // Ignore, as above.
  }
}

function local(): Storage | undefined {
  try {
    return window.localStorage
  } catch {
    return undefined
  }
}

function session(): Storage | undefined {
  try {
    return window.sessionStorage
  } catch {
    return undefined
  }
}

/**
 * announcementSig hashes what an announcement SAYS. This is the function the previous release used
 * (FNV-1a over the UTF-16 code units of a JSON.stringify), kept byte-identical so a dismissal made
 * before the upgrade still matches after it.
 */
export function announcementSig(a: Pick<Announcement, 'level' | 'title' | 'content'>): string {
  const raw = JSON.stringify({ level: a.level, title: a.title, content: a.content })
  let hash = 2166136261
  for (let i = 0; i < raw.length; i += 1) {
    hash ^= raw.charCodeAt(i)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}

// A banner dismissal and a popup dismissal are different acts on the same announcement — closing
// the banner should not also stop the popup — so they get different keys under the same map.
const popupKey = (id: number) => String(id)
const bannerKey = (id: number) => `b:${id}`

function mapKey(user: string) {
  return DISMISSED_PREFIX + user.trim().toLowerCase()
}

function loadMap(user: string): DismissMap {
  const raw = read(local(), mapKey(user))
  if (!raw) return {}
  try {
    const parsed = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {}
    const out: DismissMap = {}
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      const e = v as Partial<Dismissal>
      if (e && typeof e.sig === 'string' && typeof e.at === 'number') out[k] = { sig: e.sig, at: e.at }
    }
    return out
  } catch {
    return {} // corrupt or written by a future version: start over rather than throw in the shell
  }
}

/**
 * gc trims the map by AGE and COUNT — never by "this id is not in the current payload".
 *
 * That inviting shortcut is wrong, and quietly: the reader feed already filters by enabled, by
 * display window and by audience, so "absent" has at least four meanings and only one of them is
 * "deleted". An announcement switched off for a day, one whose window closed overnight, or a
 * reader whose OU was moved and moved back, would all have their dismissals erased and be
 * re-announced to everyone the next morning. The server's 50-row cap already bounds the only real
 * growth risk this would have addressed.
 */
function gc(map: DismissMap, now: number): DismissMap {
  const entries = Object.entries(map)
    .filter(([, v]) => now - v.at < MAX_AGE_MS)
    .sort((a, b) => b[1].at - a[1].at)
    .slice(0, MAX_ENTRIES)
  return Object.fromEntries(entries)
}

function saveMap(user: string, map: DismissMap, now: number) {
  write(local(), mapKey(user), JSON.stringify(gc(map, now)))
}

/**
 * migrateLegacyDismissal carries the single pre-upgrade dismissal over, once.
 *
 * The old key held a bare signature with no id beside it, so the only way to find out which
 * announcement it belonged to is to compare it against the ones on offer — which works, because
 * the imported row keeps the level, title and body the hash was taken over. Matched or not, the
 * old key is then deleted: leaving it would make this run on every mount forever.
 *
 * It MUST be called with the reader's whole feed, which is why it lives here and is driven from the
 * provider rather than from loadDismissals. Three components read dismissals — the strip, the home
 * band and the popup — and each sees only its own slice; whichever rendered first would consume the
 * legacy key while comparing it against a handful of announcements it happens to draw, or against
 * none at all, and every reader who had clicked "don't show again" would be interrupted on upgrade
 * day. Exactly what this function exists to prevent.
 *
 * Callers pass a non-empty list: with nothing on offer there is nothing to match, and deleting the
 * key then would throw the dismissal away before it could ever be claimed.
 */
export function migrateLegacyDismissal(user: string, items: Announcement[], now = Date.now()) {
  const legacy = read(local(), LEGACY_KEY)
  if (!legacy || !items.length) return
  remove(local(), LEGACY_KEY)
  if (read(local(), mapKey(user))) return // already migrated, or the reader has newer dismissals
  const hit = items.find((a) => announcementSig(a) === legacy)
  if (hit) saveMap(user, { [popupKey(hit.id)]: { sig: legacy, at: now } }, now)
}

/**
 * A reader's dismissal state, resolved against the announcements currently on offer. Built once
 * per payload; the callers ask it questions and hand back a new one after each write.
 */
export interface DismissalState {
  /** Has this reader silenced this announcement's popup for good? */
  popupDismissed: (a: Announcement) => boolean
  /** Has this reader closed this announcement's banner for good? */
  bannerDismissed: (a: Announcement) => boolean
  /** Has this reader acknowledged this popup in THIS browser session? */
  seenThisSession: (a: Announcement) => boolean
}

export function loadDismissals(user: string, items: Announcement[]): DismissalState {
  const map = loadMap(user)
  const seen = new Set(readSeen(user))
  const matches = (key: string, a: Announcement) => map[key]?.sig === announcementSig(a)
  return {
    popupDismissed: (a) => matches(popupKey(a.id), a),
    bannerDismissed: (a) => matches(bannerKey(a.id), a),
    seenThisSession: (a) => seen.has(`${a.id}:${announcementSig(a)}`),
  }
}

/** Persist "don't show this popup again" for one announcement, at its current wording. */
export function dismissPopup(user: string, a: Announcement, now = Date.now()) {
  const map = loadMap(user)
  map[popupKey(a.id)] = { sig: announcementSig(a), at: now }
  saveMap(user, map, now)
}

/** Persist "I closed this banner" for one announcement, at its current wording. */
export function dismissBanner(user: string, a: Announcement, now = Date.now()) {
  const map = loadMap(user)
  map[bannerKey(a.id)] = { sig: announcementSig(a), at: now }
  saveMap(user, map, now)
}

// "Got it" is weaker than "don't show again": it stops the popup for this browser session and
// nothing more. sessionStorage expires it on its own, so it needs no garbage collection.
function readSeen(user: string): string[] {
  const raw = read(session(), SEEN_PREFIX + user.trim().toLowerCase())
  if (!raw) return []
  try {
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed.filter((v): v is string => typeof v === 'string') : []
  } catch {
    return []
  }
}

export function markSeen(user: string, a: Announcement) {
  const key = `${a.id}:${announcementSig(a)}`
  const next = readSeen(user)
  if (!next.includes(key)) next.push(key)
  write(session(), SEEN_PREFIX + user.trim().toLowerCase(), JSON.stringify(next))
}
