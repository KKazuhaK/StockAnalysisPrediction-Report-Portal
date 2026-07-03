# Downloadable apps

Runtime-installable, sandboxed iframe apps for the portal. See the design in
[docs/adr/0003-downloadable-apps.md](../docs/adr/0003-downloadable-apps.md).

An **app** is a `.zip` bundle: an `app.json` manifest plus self-contained frontend
files. An admin installs it under **Manage → Apps**; it then appears as a card in
the **Apps** hub for every user and opens inside a sandboxed `<iframe>`.

## Manifest (`app.json`)

```json
{
  "id": "demo-symbols",     // slug: a-z 0-9 _ - (also the URL + asset path)
  "name": "Symbols demo",
  "icon": "🔎",             // optional emoji shown on the card
  "version": "1.0.0",
  "entry": "index.html",    // HTML entry point inside the bundle
  "scopes": ["query"]       // API scopes; only "query" is granted in phase 1
}
```

## Talking to the portal (the bridge)

The iframe runs with `sandbox="allow-scripts"` — a null origin, so it cannot read
the host DOM, the session cookie, or localStorage. It reaches the portal only by
posting a message to its parent; the host validates the request against the app's
granted scopes and performs the `/api/v1` call with a short-lived scoped token the
iframe never sees.

```js
// request  →  host
parent.postMessage({ type: 'rp:api', reqId: 1, method: 'GET', path: '/api/v1/symbols?limit=20' }, '*')

// host  →  app
window.addEventListener('message', (e) => {
  const m = e.data
  if (m.type === 'rp:api:result' && m.reqId === 1) { /* m.ok, m.status, m.data */ }
  if (m.type === 'rp:init') { /* m.theme = { dark, colorPrimary, colorBg, colorText } */ }
})
```

Phase 1 grants read-only access (`GET /api/v1/*`, `query` scope). Write scopes and
an install-time permission prompt are phase 2.

## The demo

[`demo-symbols/`](demo-symbols) is a one-file app that lists stocks with reports.
Install `demo-symbols.zip` under **Manage → Apps** to try the whole chain:
empty hub → install → a card appears → open it → it reads the API through the bridge.

Rebuild the zip after editing:

```sh
cd demo-symbols && zip -j -X ../demo-symbols.zip app.json index.html
```
