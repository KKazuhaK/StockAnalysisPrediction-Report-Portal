# 3. Downloadable apps (iframe app platform)

## Status

Accepted (design only — not yet implemented) — 2026-07-03

## Context

The Apps hub (ADR 0002 introduced it as an idea; the hub itself shipped with the
batch-run app as a built-in card) should eventually be an app *center*: empty by
default, with apps you download and install — not a hardcoded list. The user wants
third-party / community apps to be installable at runtime without recompiling the
portal.

The hard part: an "app" has a UI and often logic, and it must run inside a single
Go binary + React SPA, safely, without a rebuild. The four ways to do that:

| Approach | Isolation | No recompile | Integration | Verdict |
| --- | --- | --- | --- | --- |
| **iframe embed + API** | ✅ sandboxed | ✅ | medium (postMessage / API) | **chosen** — how Shopify / Atlassian Connect / Slack do third-party apps |
| Module Federation (remote React modules) | ❌ runs in host context, can read the session/DOM | ✅ | high | rejected — untrusted third-party code with full host access + React/antd version lock-in |
| WASM backend sandbox | ✅ (backend only) | ✅ | low | rejected — WASM can't do UI; host must expose every capability explicitly; heavy |
| Out-of-process (subprocess / container per app) | ✅ | ✅ | high | rejected — ops burden of managing app processes |

## Decision

**An app is a downloadable bundle (manifest + self-contained frontend assets) that
the host renders in a sandboxed iframe and that talks to the portal only through
`/api/v1` with a scoped token.** No host recompile, no third-party code in the host
page's context.

- **App = manifest + assets:**
  `app.json = { id, name, icon, version, entry: "index.html", scopes: ["query", "webhooks", …] }`
  plus the app's own `index.html` / JS / CSS.
- **Rendering:** the Apps hub shows a card per installed app; opening it routes to a
  `<iframe sandbox>` loading the app's `entry`. The iframe cannot reach the host DOM
  or the session cookie.
- **API access:** the iframe reaches the portal via `/api/v1`, authorized by a
  **scoped, short-lived token** the host issues for that app (limited to the app's
  declared scopes), passed over a `postMessage` handshake. The host mediates; it
  never hands the raw session cookie to the iframe.
- **Distribution:** apps are downloaded/installed like plugins (ADR 0001) — from a
  GitHub market or a manual upload — and served by the portal under
  `/app-assets/{id}/`. Admin-only install.
- **First-party apps stay native.** Batch-run has a Go engine + deep UI and remains
  a compiled-in app; it is one card alongside installed iframe apps. This mirrors
  Grafana's "built-in + installed plugins" model.

Principle: **downloadable apps run beside the host (sandboxed iframe + scoped API),
never inside it.** This is the same "extend by talk-to-it, not code-in-it" stance
as ADR 0002, applied to UIs.

## Alternatives rejected

See the table above. The short version: Module Federation gives the nicest
integration but hands untrusted code the user's session — unacceptable. WASM and
out-of-process are either UI-incapable or operationally heavy. iframe + scoped API
is the only approach that is simultaneously safe for third-party code, recompile-
free, genuinely "download and install", and realistic for a small self-hosted tool.

## Consequences

- Installed apps are visually separate (their own styling) unless they opt into the
  host's design tokens — the host can pass theme tokens over the postMessage
  handshake so an app can match light/dark and the primary colour.
- The scoped-token + postMessage bridge is a real security boundary that must be
  designed carefully (scope enforcement, token expiry, install-time permission
  prompt, host allowlist for remote app assets). Install is admin-only.
- An app needing server-side compute must either use the portal's API or host its
  own backend; the portal does not run app backend code (that would be the deferred
  WASM/subprocess rung).

## Rollout (when built)

1. **Phase 1 — proof of concept:** an app registry table, install (upload a bundle),
   the Apps hub rendering installed apps as sandboxed iframe cards, a minimal
   postMessage → `/api/v1` bridge with a scoped token, and one demo app. Proves the
   core chain: empty hub → install → a card appears → open it → it works.
2. **Phase 2:** a GitHub app market (download like plugins), scoped tokens with
   expiry, an install-time permission prompt, and a theme handshake into the iframe.
3. **Phase 3:** polish, a small developer SDK, and docs.

## Appendix — layering

`Provider` plugins (ADR 0001) configure a built-in app's backends (e.g. Dify
workflows for batch-run). **Apps** (this ADR) are user-facing features. Both are
"downloadable", at different layers: a plugin is data a built-in app consumes; an
app is a sandboxed iframe UI that consumes the portal's API.
