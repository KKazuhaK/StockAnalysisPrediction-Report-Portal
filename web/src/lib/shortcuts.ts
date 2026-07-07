// App shortcuts: an entry-link (入口管理) button can trigger an internal action — open Run
// Analysis, the queue, the assistant, the apps hub — instead of opening a URL. To avoid a
// schema change, a shortcut is stored in the link's `url` as "rp:<key>"; everything else is
// interpreted here. Run Analysis and the queue live as a modal/drawer in AppLayout, so those
// two fire a window event that AppLayout listens for; chat/apps are plain routes.

export const SHORTCUT_PREFIX = 'rp:'

export type ShortcutKey = 'run-analysis' | 'queue' | 'chat' | 'apps'

export interface AppShortcut {
  key: ShortcutKey
  labelKey: string // i18n key for the display name (reuses the nav labels)
  route?: string // navigate here (chat / apps)
  event?: string // else dispatch this window event; AppLayout opens the modal/drawer
  requiresRun: boolean // gated by PermRunBatch (hidden from users who can't run batch)
}

export const RUN_ANALYSIS_EVENT = 'rp:open-run-analysis'
export const QUEUE_EVENT = 'rp:open-queue'

export const APP_SHORTCUTS: AppShortcut[] = [
  { key: 'run-analysis', labelKey: 'nav.runAnalysis', event: RUN_ANALYSIS_EVENT, requiresRun: true },
  { key: 'queue', labelKey: 'nav.queue', event: QUEUE_EVENT, requiresRun: true },
  { key: 'chat', labelKey: 'nav.chat', route: '/chat', requiresRun: true },
  { key: 'apps', labelKey: 'nav.apps', route: '/apps', requiresRun: false },
]

// shortcutOfUrl returns the shortcut a link points to, or undefined for a plain URL link.
export function shortcutOfUrl(url?: string): AppShortcut | undefined {
  if (!url || !url.startsWith(SHORTCUT_PREFIX)) return undefined
  const key = url.slice(SHORTCUT_PREFIX.length)
  return APP_SHORTCUTS.find((s) => s.key === key)
}

export function shortcutUrl(key: ShortcutKey): string {
  return SHORTCUT_PREFIX + key
}

// triggerShortcut performs a shortcut's action: navigate for a route, else fire its event.
export function triggerShortcut(sc: AppShortcut, navigate: (to: string) => void) {
  if (sc.route) navigate(sc.route)
  else if (sc.event) window.dispatchEvent(new Event(sc.event))
}
