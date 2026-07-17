// Package app is the application core of report-portal: it loads config, opens the store,
// bootstraps first-run state, wires every HTTP route onto one net/http ServeMux, and serves the
// embedded React SPA. The reusable domain logic lives in sibling packages (internal/batch,
// internal/queue, internal/dify, internal/webhook, internal/mail, internal/legacy,
// internal/config); this package is the HTTP-handler + persistence + wiring layer that glues them
// to the Server and Store. It is intentionally ONE package — Go favors cohesive packages over
// many tiny ones, and a type's methods must live in its own package — so files are grouped by a
// name-prefix convention (batch_*, chat_*, apps_*, webhook_*, user_*, dify_*) with a consistent
// suffix scheme: *_api.go = HTTP handlers, *_store.go = persistence, *_run.go = orchestration.
//
// # File map
//
// Bootstrap & HTTP plumbing
//   - server.go       config load, first-run bootstrap, ALL route registration, serve loop
//   - spa.go          serve the embedded SPA (index.html for client-side deep links)
//   - gzip.go         transparent gzip middleware for text responses
//   - pwa.go          PWA manifest + icon
//   - site_assets.go  serve admin-uploaded site branding assets (logo, favicon)
//   - pdf.go          server-side PDF export (the one remaining HTML template)
//
// API route families (see the route-layering comment in server.go and CLAUDE.md)
//   - apiui.go   browser SPA API — cookie session (rp_session), requireUserJSON
//   - apiv1.go   versioned machine API /api/v1 — Bearer token (ADR 0002)
//   - openapi.go the OpenAPI 3.1 spec served for the v1 API
//     (v1 is the entire machine surface: the pre-v1 /api/reports… token API it grew out of
//     was retired once Dify's tool schemas spoke only v1. /api/symbols, the omnibox's
//     autocomplete, is all that stayed behind in server.go — and it is a browser route.)
//
// Store & schema (dual-driver SQLite/Postgres, no ORM)
//   - store.go            the Store type, base schema (baseSchemaStmts), dialect helpers
//   - migrate.go          migration machinery + ensureColumns (additive-column reconcile)
//   - migrate_v1_to_v2.go the disposable v0.1→v0.2 fold step (deleted at the next major)
//
// Reports: grouping, reading, export
//   - group.go      collapse a run (symbol+date) into one card
//   - md.go         GitHub-flavored Markdown renderer
//   - day_export.go "all of a stock's reports on one date" bundle export
//   - names.go      A-share company-name fetch + ingest-time snapshot
//
// Batch / run queue (ADR 0001/0004/0006/0008/0011/0014)
//   - batch_api.go     admin HTTP surface (/api/admin/batch/*) + run-queue config
//   - batch_run.go     orchestration: scheduler, provider build, resume/reconcile
//   - batch_store.go   job/item persistence
//   - run_preset.go    preset low-peak scheduling windows (ADR 0014)
//   - dify_api.go      Dify-native target configuration (ADR 0006)
//   - dify_provider.go the dify.Client → batch.Provider adapter (stream / reconcile / stop)
//
// Recurring tasks + shared cadence (ADR 0018)
//   - recurring.go       cadence loop (recurringLoop) that fires a saved rows template into the queue
//   - recurring_api.go   admin HTTP surface (/api/admin/batch/recurring/*)
//   - recurring_store.go recurring_tasks + recurring_runs persistence
//   - cadence.go         shared daily/weekly/monthly due-check + next-occurrence (cleanup + recurring)
//
// Storage retention cleanup (ADR 0017)
//   - cleanup.go       retention engine + the daily/weekly/monthly cadence loop (cleanupLoop)
//   - cleanup_api.go   admin console HTTP surface (/api/admin/cleanup/*)
//   - cleanup_store.go usage stats, retention delete queries, cleanup_runs audit log
//
// Interactive chat / assistant (ADR 0012)
//   - chat_api.go   HTTP handlers (send / stop / history / conversations)
//   - chat_gate.go  concurrency ceiling + in-flight registry + admin live view + stop
//   - chat_store.go conversation-index persistence (Dify owns the message history)
//
// Downloadable iframe apps (ADR 0003)
//   - apps_api.go    install / list / run HTTP surface
//   - apps_market.go the GitHub-hosted app-index browser
//   - apps_store.go  installed-app persistence
//   - apps_token.go  the app-bridge scoped-token minting
//
// Outbound webhooks (ADR 0002)
//   - webhook_api.go   admin management HTTP surface (PermManage)
//   - webhook_run.go   fireEvent — signed event dispatch with retry
//   - webhook_store.go subscription persistence
//
// Accounts, RBAC, auth
//   - user.go           the users table + authentication
//   - user_admin.go     extended profile persistence (organizational groups, model B)
//   - user_admin_api.go enterprise user-admin HTTP handlers
//   - roles.go          Perm* constants + role→permission registry (RBAC-lite)
//   - password_reset.go stateless HMAC email password reset
//   - tickets.go        urgent-run priority tickets — per-user urgent-run quota (ADR 0005)
//
// Notifications
//   - email.go emails a submitter when their batch job finishes (opt-in)
//
// Design note: this package's two central types — Store (~178 methods) and Server (~215
// methods) — are large by design for a single-binary app. Decomposing them (per-domain
// repositories; cohesive sub-handlers) is a deliberate, ADR-worthy future refactor, not a file
// move: because Go keeps a type's methods in its own package, splitting these subsystems into
// subfolders would re-architect Store and Server, not merely relocate code.
package app
