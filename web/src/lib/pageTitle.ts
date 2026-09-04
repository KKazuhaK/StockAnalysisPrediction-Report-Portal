// The browser tab said the same thing on every page. With half a dozen tabs open on one portal —
// which is how one report gets compared against another, or a run watched while an admin page is
// edited — they are indistinguishable, and browser history becomes a list of identical entries.
//
// Labels come from the same i18n keys the navigation uses, so a page renamed in the menu is renamed
// in the tab, and it is translated with the rest of the UI rather than pinned to the language the
// shell happened to be built in.

// The manage pages, keyed by their last path segment, using the very labels the left rail renders.
const MANAGE: Record<string, string> = {
  site: 'settings.general',
  announcement: 'nav.announcement',
  email: 'nav.email',
  links: 'nav.links',
  types: 'nav.types',
  versions: 'nav.versions',
  users: 'nav.users',
  sso: 'nav.sso',
  security: 'nav.security',
  tokens: 'settings.tokens',
  batch: 'nav.batchAdmin',
  runqueue: 'nav.runQueue',
  rundefaults: 'nav.runDefaults',
  assistant: 'nav.chat',
  apps: 'nav.appsAdmin',
  webhooks: 'nav.webhooks',
  apidoc: 'settings.apidoc',
  storage: 'nav.storage',
  audit: 'nav.audit',
}

// An empty label means the bare site title: the home page (there is nothing to add) and the
// signed-out pages, where the site's name is the only useful thing to say before anyone is in.
const EXACT: Record<string, string> = {
  '/': '',
  '/login': '',
  '/reset': '',
  '/register': '',
  '/forgot': '',
  '/verify': '',
  '/account': 'nav.account',
  '/review': 'nav.review',
  '/apps': 'nav.apps',
  '/apps/batch': 'nav.batch',
  '/apps/recurring': 'nav.recurring',
  '/queue': 'nav.queue',
  '/chat': 'nav.chat',
  '/report/new': 'reportEditor.titleNew',
}

// routeTitle names the page at `pathname`, translated. '' means "no label" — the caller shows the
// site title alone.
export function routeTitle(pathname: string, t: (k: string) => string): string {
  const p = pathname.replace(/\/+$/, '') || '/'
  if (p in EXACT) {
    const key = EXACT[p]
    return key ? t(key) : ''
  }
  if (/^\/report\/[^/]+\/edit$/.test(p)) return t('reportEditor.titleEdit')
  // A reader page is named by what it is showing, which is the only thing that tells two open
  // reports apart — and telling them apart is the whole reason the tab has a title.
  if (p.startsWith('/stock/') || p.startsWith('/run/')) {
    return safeDecode(p.split('/')[2] ?? '')
  }
  if (p === '/manage') return t('nav.manage')
  if (p.startsWith('/manage/')) {
    const key = MANAGE[p.slice('/manage/'.length)]
    return t(key ?? 'nav.manage')
  }
  return ''
}

// pageTitle composes what goes in the tab. The specific part comes first, because a tab is narrow
// and the site name is the half every tab shares — truncating that loses nothing.
export function pageTitle(label: string, site: string): string {
  const l = label.trim()
  return l ? `${l} · ${site}` : site
}

// A path segment is percent-encoded and can be malformed if someone typed the URL by hand;
// decodeURIComponent throws on those, and a title is never worth an exception.
function safeDecode(seg: string): string {
  try {
    return decodeURIComponent(seg)
  } catch {
    return seg
  }
}
