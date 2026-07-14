// Built-in (compiled-in) apps — the first-party cards in the Apps hub, distinct from installed
// downloadable iframe apps (ADR 0003). Kept in one place so the hub, the entry-button target picker,
// and the shortcut router all agree on the list, each app's route, and the permission it requires.
export interface BuiltinApp {
  key: string // stable slug (also the entry-button pin id, as `builtin:<key>`)
  perm: string // permission a viewer needs to see/open it ('' = everyone)
  to: string // the route the app lives at
  titleKey: string // i18n key for its name
  descKey: string // i18n key for its hub-card description
}

// NOTE: `perm` drives BOTH hub-card visibility and entry-button visibility (via shortcutPerm). The
// destination route's own guard is a separate <RequirePerm> in App.tsx — keep the two in sync: if you
// change an app's `perm` here, update its route guard there too (and vice-versa).
export const BUILTIN_APPS: BuiltinApp[] = [
  { key: 'batch', perm: 'run_batch', to: '/apps/batch', titleKey: 'nav.batch', descKey: 'apps.batchDesc' },
  { key: 'recurring', perm: 'run_batch', to: '/apps/recurring', titleKey: 'nav.recurring', descKey: 'apps.recurringDesc' },
]

// An entry-button "apps" shortcut pins to a specific app: a downloadable app by its slug id, or a
// built-in app by `builtin:<key>` (this prefix disambiguates the two so a downloadable app whose id
// happens to equal a built-in key can't collide).
export const BUILTIN_PIN_PREFIX = 'builtin:'

export function builtinAppByKey(key: string): BuiltinApp | undefined {
  return BUILTIN_APPS.find((a) => a.key === key)
}
