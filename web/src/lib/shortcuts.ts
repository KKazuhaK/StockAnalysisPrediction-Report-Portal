// App shortcuts: an entry-link (入口管理) button can trigger an internal action — open Run
// Analysis, the queue, the assistant, the apps hub — instead of opening a URL. To avoid a
// schema change, a shortcut is stored in the link's `url` as "rp:<key>"; everything else is
// interpreted here. A shortcut can optionally carry a target so the action opens *pre-selected*
// on a specific Dify target / installed app: "rp:<key>:<param>" (param is opaque — a numeric
// Dify-target id for run-analysis/chat, a string app id for apps). Run Analysis and the queue
// live as a modal/drawer in AppLayout, so those two fire a window event that AppLayout listens
// for; chat/apps are plain routes.

export const SHORTCUT_PREFIX = 'rp:'

export type ShortcutKey = 'run-analysis' | 'queue' | 'chat' | 'apps'

export interface AppShortcut {
  key: ShortcutKey
  labelKey: string // i18n key for the display name (reuses the nav labels)
  route?: string // navigate here (chat / apps)
  event?: string // else dispatch this window event; AppLayout opens the modal/drawer
  requiresRun: boolean // gated by PermRunBatch (hidden from users who can't run batch)
  hasTarget: boolean // can be pinned to a specific target/app (shows the target picker in the editor)
}

// The resolved form of a shortcut link: the action plus its optional pinned target (a string,
// since app ids are slugs while run/chat target ids are numeric — callers coerce as needed).
export interface ResolvedShortcut {
  shortcut: AppShortcut
  param?: string
}

export const RUN_ANALYSIS_EVENT = 'rp:open-run-analysis'
export const QUEUE_EVENT = 'rp:open-queue'

export const APP_SHORTCUTS: AppShortcut[] = [
  { key: 'run-analysis', labelKey: 'nav.runAnalysis', event: RUN_ANALYSIS_EVENT, requiresRun: true, hasTarget: true },
  { key: 'queue', labelKey: 'nav.queue', event: QUEUE_EVENT, requiresRun: true, hasTarget: false },
  { key: 'chat', labelKey: 'nav.chat', route: '/chat', requiresRun: true, hasTarget: true },
  { key: 'apps', labelKey: 'nav.apps', route: '/apps', requiresRun: false, hasTarget: true },
]

// shortcutOfUrl returns the shortcut a link points to (plus any pinned target), or undefined
// for a plain URL link. The key never contains ':' (run-analysis, queue, chat, apps), so we
// split on the FIRST colon only — an app id slug may itself contain ':' and must stay intact.
export function shortcutOfUrl(url?: string): ResolvedShortcut | undefined {
  if (!url || !url.startsWith(SHORTCUT_PREFIX)) return undefined
  const rest = url.slice(SHORTCUT_PREFIX.length)
  const i = rest.indexOf(':')
  const key = i < 0 ? rest : rest.slice(0, i)
  const param = i < 0 ? undefined : rest.slice(i + 1)
  const shortcut = APP_SHORTCUTS.find((s) => s.key === key)
  return shortcut ? { shortcut, param: param || undefined } : undefined
}

// shortcutUrl builds the stored url for a shortcut, optionally pinned to a target.
export function shortcutUrl(key: ShortcutKey, param?: string | number): string {
  const p = param == null ? '' : String(param).trim()
  return SHORTCUT_PREFIX + key + (p ? ':' + p : '')
}

// triggerShortcut performs a shortcut's action: navigate for a route, else fire its event.
// A pinned param deep-links: chat opens that assistant, apps opens that installed app, and
// run-analysis carries the target id in the event so the modal opens pre-selected.
export function triggerShortcut(sc: AppShortcut, navigate: (to: string) => void, param?: string) {
  if (sc.route) {
    if (param && sc.key === 'chat') navigate(`${sc.route}?target=${encodeURIComponent(param)}`)
    else if (param && sc.key === 'apps') navigate(`/apps/x/${encodeURIComponent(param)}`)
    else navigate(sc.route)
  } else if (sc.event) {
    if (param) window.dispatchEvent(new CustomEvent(sc.event, { detail: { targetId: Number(param) } }))
    else window.dispatchEvent(new Event(sc.event))
  }
}
