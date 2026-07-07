// Bump this on any change to the caching strategy (or to force a one-time flush of stale
// cached assets): `activate` deletes every cache whose name isn't the current one, so a new
// version wipes the old app-shell/asset cache on the next deploy.
const CACHE_NAME = 'report-portal-pwa-v4'
const APP_SHELL = ['/', '/favicon.svg']

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.addAll(APP_SHELL))
      .catch(() => undefined)
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    Promise.all([
      caches.keys().then((keys) => Promise.all(keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key)))),
      self.clients.claim(),
    ]),
  )
})

// Strategy: NETWORK-FIRST for everything, cache only as an offline fallback.
//
// The previous version served /assets/ cache-first, which could pin the app to a stale
// build: after a deploy the hashed chunk names change, and a cached old index.html/bundle
// would keep requesting old chunks the server no longer has -> a blank page. Because the
// Go server marks /assets/ `immutable`, the browser HTTP cache already serves repeat asset
// loads instantly from disk with no network round-trip, so network-first is just as fast
// here while guaranteeing the app shell and its chunks always come from the same (current)
// build when online. The SW cache is now purely an offline safety net.
self.addEventListener('fetch', (event) => {
  const { request } = event
  if (request.method !== 'GET') return

  const url = new URL(request.url)
  if (url.origin !== self.location.origin) return
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/report/')) return

  if (request.mode === 'navigate') {
    event.respondWith(networkFirst(request, '/'))
    return
  }

  if (
    url.pathname.startsWith('/assets/') ||
    url.pathname === '/favicon.svg' ||
    url.pathname === '/manifest.webmanifest' ||
    url.pathname === '/pwa-icon' ||
    url.pathname.startsWith('/site-assets/')
  ) {
    event.respondWith(networkFirst(request))
  }
})

async function networkFirst(request, fallbackUrl) {
  const cache = await caches.open(CACHE_NAME)
  try {
    const response = await fetch(request)
    if (response.ok && response.type === 'basic') {
      await cache.put(request, response.clone())
    }
    return response
  } catch (err) {
    const cached = await cache.match(request)
    if (cached) return cached
    if (fallbackUrl) {
      const fallback = await cache.match(fallbackUrl)
      if (fallback) return fallback
    }
    throw err
  }
}
