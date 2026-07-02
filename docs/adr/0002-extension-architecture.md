# 2. Extension architecture — how third parties extend the portal

## Status

Accepted — 2026-07-03

## Context

We want enthusiasts and third-party developers to add features we will not build
ourselves — a daily email digest, a Feishu/Slack push on new reports, an on-site
AI chat widget, archiving reports to a cloud drive, and things we have not thought
of. The goal is to leave **entry points**, not to ship every integration.

The tempting path is an in-process code-plugin framework (WordPress hooks, Obsidian
/ VS Code extension APIs): a plugin is code loaded into our process that calls our
API. For a small self-hosted Go service this is the wrong trade: it forces a
frozen, versioned, documented plugin API we can never casually change, a security
sandbox for third-party code, and host-stability guarantees — and Go has no
ergonomic runtime code loading (`.so` is brittle; WASM/subprocess are heavy). It is
the single biggest refactor magnet.

## Decision

Contributors extend the portal by **talking to it, listening to it, and embedding
into it — never by running code inside it.** Four standard, decoupled extension
points:

1. **Inbound API** — `/api/v1/*` with Bearer tokens (already built). Read/write
   portal data. A contributor's own cron can build a daily email from it.
2. **Outbound events + webhooks** — the portal POSTs a signed JSON payload to
   subscriber URLs when something happens (`report.ingested`, `batch.job.finished`,
   …). A contributor writes a small receiver to push to Feishu/Slack/email. Their
   code runs in *their* service, not ours.
3. **Frontend embed slots** — an admin-configured HTML/script/iframe snippet
   rendered in a page slot (e.g. footer / bottom-right), the way any site adds
   Intercom/Crisp/analytics. Hosts the on-site AI chat widget without a plugin
   framework.
4. **Batch providers** — declarative manifests that trigger workflow backends
   (see ADR 0001).

Principle: **extend by API-in / webhook-out / embed-in, not code-in.**

## Alternatives rejected

- **In-process code-plugin framework (WordPress / Obsidian / VS Code style)** — a
  permanent, expensive commitment (frozen plugin API, sandbox, versioning, host
  stability) and the main refactor magnet. Go also lacks good runtime loading.
  A future WASM "rung" remains possible, and it would consume the *same* events +
  API substrate built here — so nothing is wasted by deferring it.
- **Building each integration ourselves (email, Feishu, cloud drive)** — not our
  job. We provide the entry points; contributors build the integrations.

## Consequences

- Integrations run **out-of-process** (in the contributor's own service), so a bad
  integration cannot crash or slow the portal, and we never freeze an in-process
  API.
- Webhook delivery is **best-effort with retry + an HMAC signature**; subscribers
  must be idempotent and verify the signature.
- The embed slot executes admin-configured third-party script in the page — an
  XSS/trust surface. It is admin-only and must be documented as such.
- This is additive to, and independent of, the batch engine (ADR 0001). Adding an
  extension point never requires reworking existing ones.

## Appendix — shape

Extension points and their status:

| Point            | Direction | Status   | Example use                     |
| ---------------- | --------- | -------- | ------------------------------- |
| `/api/v1` + token | inbound  | built    | daily email from report data    |
| events + webhooks | outbound | building | Feishu/Slack/email on new report |
| embed slot        | in-page  | future   | bottom-right AI chat widget     |
| batch providers   | outbound | built    | trigger a Dify workflow         |

Event catalogue (initial):

- `report.ingested` — a report was upserted via `/api/reports` or `/api/v1/reports`.
- `batch.job.finished` — a batch job reached a terminal state.

Delivery contract:

```
POST <subscriber url>
Content-Type: application/json
X-Report-Portal-Event: report.ingested
X-Report-Portal-Signature: sha256=<hex HMAC-SHA256 of body, keyed by the webhook secret>

<event payload JSON>
```
