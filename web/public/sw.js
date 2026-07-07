// Versioned caching service worker.
//
// The cache name embeds the build version (the Go server injects the commit in place of
// __RP_SW_VERSION__ when it serves /sw.js), so EVERY deploy produces a different SW whose
// `activate` deletes all other caches — the old build's assets can never linger and strand
// a returning visitor on a stale shell (the bug that blanked pages before). Between deploys
// the version is stable, so hashed assets are cache-first (instant, offline-capable).
//
// Strategy:
//   - /api/, /report/  -> never intercepted: always live from the network (real-time data).
//   - navigation (the HTML shell) -> network-first: always the current index.html when
//     online, so it references the current build's chunks; cached shell only as an offline
//     fallback.
//   - /assets/ (content-hashed, immutable) -> cache-first: fast, and safe because the cache
//     is per-version and purged on every deploy.
//   - favicon / manifest / pwa-icon / site-assets -> network-first (branding can change).
const CACHE_NAME = 'rp-cache-__RP_SW_VERSION__'
const SHELL = '/'

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches
      .open(CACHE_NAME)
      .then((cache) => cache.add(SHELL))
      .catch(() => undefined)
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    Promise.all([
      caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k)))),
      self.clients.claim(),
    ]),
  )
})

self.addEventListener('fetch', (event) => {
  const { request } = event
  if (request.method !== 'GET') return

  const url = new URL(request.url)
  if (url.origin !== self.location.origin) return
  // Real-time data is never cached.
  if (url.pathname.startsWith('/api/') || url.pathname.startsWith('/report/')) return

  if (request.mode === 'navigate') {
    event.respondWith(networkFirst(request, SHELL))
    return
  }

  if (url.pathname.startsWith('/assets/')) {
    event.respondWith(cacheFirst(request))
    return
  }

  if (
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
    if (response.ok && response.type === 'basic') await cache.put(request, response.clone())
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

async function cacheFirst(request) {
  const cache = await caches.open(CACHE_NAME)
  const cached = await cache.match(request)
  if (cached) return cached
  const response = await fetch(request)
  if (response.ok && response.type === 'basic') await cache.put(request, response.clone())
  return response
}
