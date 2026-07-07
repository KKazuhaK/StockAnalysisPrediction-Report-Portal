// Minimal, cache-free service worker.
//
// It exists only so the app is installable (Chrome requires a registered service worker
// with a fetch handler). It does NO caching: earlier versions cached the app shell/assets,
// which repeatedly stranded returning visitors on an old build after a deploy — the cached
// index.html/bundle kept requesting old hashed chunks the server no longer had, blanking the
// page. Removing caching entirely makes that impossible; the browser's own HTTP cache still
// serves immutable /assets/ from disk (fast) and revalidates the no-cache index.html.
//
// Bump the version to change the bytes so browsers pick up this script; `activate` wipes
// every old cache once, clearing any stale shell left by a previous cache-first version.
const SW_VERSION = 'report-portal-pwa-v5'

self.addEventListener('install', () => self.skipWaiting())

self.addEventListener('activate', (event) => {
  event.waitUntil(
    Promise.all([
      caches.keys().then((keys) => Promise.all(keys.map((key) => caches.delete(key)))),
      self.clients.claim(),
    ]),
  )
})

// A no-op-ish fetch handler: navigations go straight to the network (a live fetch, never a
// cached shell), everything else uses the browser default. Having a fetch handler at all is
// what keeps the app installable.
self.addEventListener('fetch', (event) => {
  if (event.request.mode === 'navigate') {
    event.respondWith(fetch(event.request))
  }
})
