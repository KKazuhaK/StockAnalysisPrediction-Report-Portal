# ADR 0009 — Batch becomes a first-class app on the agreed API

## Context

The batch-run feature is split across two places that don't match how we want apps to work:

- **`/manage/batch`** (批量任务 tab) — the target **configuration**: create Dify-workflow targets
  (新建 Dify 工作流) and import custom plugins. Admin-only (`PermManage`).
- **`/apps/batch`** — the **execution** UI (`BatchConsole`): pick a target, upload CSV, run,
  monitor. It *looks* like an app (it has a card in the Apps hub), but it is a **hardcoded React
  route** that calls `/api/admin/batch/*` directly with the session cookie. It does **not** use
  the app-bridge that installed apps use.

The apps subsystem (ADR 0002/0003) defines the **agreed interface**: an app gets a short-lived,
scoped bearer **token** (`POST /api/apps/{id}/token`), the trusted host validates each request
(`validateApiRequest`) and performs the call against **`/api/v1/*`** — never `/api/admin/*`. The
built-in batch app bypasses all of this, so it is privileged over third-party apps and the
security boundary is meaningless for it.

**Owner intent (2026-07-04):** run/queue *governance* settings stay in 管理; **批量执行 is an
app** and must go through the agreed interface. Its target configuration belongs **in the app**,
not under 管理.

## Constraints the current bridge does not meet

1. **Tokens are anonymous.** `appTokens.mint(scopes, now)` records only scopes + expiry
   ([apps_token.go:50](../../internal/app/apps_token.go)); it carries no user. But batch is
   inherently **user-attributed**: `created_by`, 加急 tickets, fair-share, and priority all key
   off the submitting user (ADR 0005/0008). A batch call through an anonymous token loses the
   identity the whole queue depends on.
2. **The bridge is read-only.** `validateApiRequest` allows **GET only**, `query` scope only,
   and the message has no `body` field ([appBridge.ts:28](../../web/src/lib/appBridge.ts)).
   Batch needs POST/DELETE (create/cancel/retry jobs; create/delete targets).
3. **Only `/api/v1/*` is reachable**; all batch endpoints today are `/api/admin/batch/*`.

## Decision

Make batch a first-class app that consumes the agreed token + `/api/v1` interface, and move its
configuration into the app. Concretely:

### 1. Scope split (owner's call)

- **运行/队列 settings stay in 管理** (`/manage/runqueue`) — they are system-wide queue
  governance (budget, reserved slots, priority weights, ticket period, default priority), not an
  app concern. No move.
- **Batch (execution + target config) is the app.** The 批量任务 config tab
  (`BatchAdminPage`: Dify targets + plugin import) moves **into** the batch app; `/manage/batch`
  goes away.

### 2. First-party app, agreed contract (not the untrusted-iframe sandbox)

The iframe sandbox exists to *contain untrusted downloadable apps*; sandboxing our own code from
itself buys nothing and costs the shared theme/router/i18n. So the built-in batch app stays a
**first-party React page**, but it accesses data **only** through the agreed contract: mint a
token via `POST /api/apps/batch/token`, then call **`/api/v1/batch/*`** with the `Bearer` token
— never `/api/admin/*`. It dogfoods the exact interface third-party apps get, with no extra
privilege. (Alternative considered: run it in the same sandboxed iframe as third-party apps —
rejected as uniformity for its own sake at a real UX/complexity cost for trusted code.)

### 3. Tokens carry the minting user + real scopes

- `appTokenEntry` gains a `user` field; `mint` records the user who opened the app.
- New grantable scopes: **`batch_execute`** (submit/cancel/retry/reprioritize jobs, read
  targets/jobs) and **`batch_admin`** (create/delete targets, probe Dify, import plugins).
- `POST /api/apps/batch/token` grants `batch_execute` only if the user has `PermRunBatch`, and
  `batch_admin` only if the user has `PermManage`. The token thus encodes *both* the identity and
  the permission ceiling; it can never exceed what the user could do with a cookie.
- The `/api/v1/batch/*` handlers attribute `created_by`/tickets/fair-share to the **token's user**
  (or the session user for a plain browser call).

### 4. Bridge gains scoped writes

- `validateApiRequest` allows **POST/DELETE** (and passes a `body`) **only** for paths under
  `/api/v1/batch/`, and only when the app holds the matching scope; everything else stays
  GET-only. The same-origin / no-traversal / `/api/v1/` checks are unchanged. The server-side
  token-scope check remains the authoritative gate.

### 5. New `/api/v1/batch/*` endpoints

Thin v1 wrappers over the existing batch handlers, guarded by `canQuery`/token-scope instead of
`requirePermJSON`, resolving the user from session-or-token:

```
GET    /api/v1/batch/targets                 query|batch_execute   (list)
POST   /api/v1/batch/targets  · /dify/probe  batch_admin           (create target / probe Dify)
DELETE /api/v1/batch/targets/{id}            batch_admin
GET    /api/v1/batch/jobs · /jobs/{id} · /queue · /tickets         batch_execute
POST   /api/v1/batch/jobs                     batch_execute         (create — created_by = token user)
POST   /api/v1/batch/jobs/{id}/cancel|retry|priority|schedule      batch_execute
```

`/api/admin/batch/*` stays for now (internal admin console / back-compat) and can be retired once
the app fully replaces it.

## Consequences

- Batch stops being privileged: it uses the same token + `/api/v1` contract as any third-party
  app, so the boundary is real and dogfooded.
- The queue keeps working end-to-end because the token now carries the user — `created_by`,
  tickets, fair-share, and priority are all preserved.
- The bridge's write relaxation is narrow (batch paths + scope + user-attributed token,
  same-origin), so read-only third-party apps are unaffected.
- Target config lives with the app, admin-gated by `batch_admin`; operators see execution only.
- No DB migration: `appTokenEntry.user` is in-memory only; scopes are additive.

## Phasing

1. **Backend:** `appTokenEntry.user` + user-aware `valid`; `batch_execute`/`batch_admin` scopes;
   `POST /api/apps/batch/token` grants by perm; `/api/v1/batch/*` handlers (reuse batch logic).
   Tests first (TDD).
2. **Bridge:** allow scoped POST/DELETE + body for `/api/v1/batch/`; register `batch` as a
   built-in app record with `[query, batch_execute, batch_admin]` (granted per-user at mint).
3. **Frontend:** batch app mints a token and calls `/api/v1/batch/*`; fold the target-config UI
   (targets + Dify probe + plugin import) into the app behind `batch_admin`; delete
   `/manage/batch`. i18n as usual.
4. **Cleanup (later):** retire `/api/admin/batch/*` once nothing calls it.
