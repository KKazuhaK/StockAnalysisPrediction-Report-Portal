# 1. Batch-run engine and plugin model

## Status

Accepted — 2026-07-02

## Context

Report generation runs on external workflow backends (today: Dify). Dify's own
"batch run" (CSV upload in the Studio UI) executes rows in the browser tab: the
run state lives in the page, so a refresh or a closed tab loses it. There is no
way to launch a large batch, leave it running overnight, and later see which rows
succeeded and which failed, nor to cancel or re-run failures.

We want this capability hosted inside the portal instead: a dedicated console that
triggers a workflow over many input rows, persists per-row state so nothing is
lost on refresh or server restart, and lets an operator watch progress, cancel,
and re-run failures.

Reports themselves are **not** produced by this feature. The existing Dify
workflow already posts each finished report back to `POST /api/reports` from an
HTTP node inside the workflow. `UpsertReport` is idempotent on `uid`
(`ON CONFLICT(uid) DO UPDATE`), so re-triggering a row never creates a duplicate
report. This batch feature only *triggers* runs and records the outcome of each
trigger.

## Decision

1. **Plugins are declarative manifests, not compiled code.** A plugin is a JSON
   manifest describing how to call a backend (request template) and how to read
   its response (status mapping). A generic interpreter executes it. Adding a new
   backend is importing a JSON file — no recompile, no restart.

2. **The engine is backend-agnostic.** `internal/batch` owns the queue, worker
   pool, retries, cancellation, DB persistence, and restart recovery. It talks to
   a `Provider` interface. The only implementation today is the manifest
   interpreter; the interface is the seam where a future sandboxed (WASM) provider
   would slot in without touching the engine.

3. **Dify is not built in.** It ships as a manifest installed from the plugin
   market, exactly like any third-party backend.

4. **The plugin market is a public GitHub repository.** The portal fetches an
   `index.json` catalogue and the manifest files over HTTPS (raw GitHub), installs
   with one click, and offers updates when a manifest's `version` changes.
   Contributing a plugin is a pull request. There is **no** offline/bundled
   fallback catalogue; when GitHub is unreachable, admins sideload a manifest file
   via manual import.

   > **Update (2026-07-04):** the separate *plugin market* has been **retired** to
   > avoid two parallel "markets". There is now exactly one GitHub market — the
   > **App market** (ADR 0003 phase 2). Dify is bundled (ADR 0006), and the manifest
   > interpreter's escape hatch for non-Dify backends survives as an admin **manual
   > import** ("custom executor"), no longer surfaced as a user-facing "plugin".

5. **Concurrency is chosen per job by the operator, capped by the admin.** The
   operator sets a per-job concurrency (1 = strictly sequential, N = N at a time)
   when launching a batch. An admin sets a global maximum (`batch_max_concurrency`
   in settings); the per-job value is clamped to it. Concurrency is fixed for the
   life of a job — to change it, cancel and relaunch.

6. **A new `operator` role.** Extends the existing `roleRegistry`. `user` views
   reports only; `operator` can *run* already-configured batch jobs; `admin`
   additionally manages targets, installs plugins, and sets the concurrency cap.
   A new permission `PermRunBatch` and a `requirePermJSON(perm)` middleware gate
   the split.

7. **Outcome is read only from the backend's own response.** Each row is
   normalised to `ok | partial | failed`. `failed`/`stopped` → failed,
   `succeeded` → ok, and `partial` only when the workflow itself emits a marker
   that the manifest's `partial_when` reads. The portal does **not** cross-check
   its own database to confirm ingestion.

## Alternatives rejected

- **Go native `plugin` (`.so`)** — requires the exact same toolchain and
  dependency versions to load, no Windows support, cannot unload; effectively
  abandoned by the community. A poor basis for "download and import".
- **Out-of-process plugins (hashicorp/go-plugin, gRPC subprocess)** — robust and
  language-agnostic (the Terraform model), but requires managing plugin processes
  and a wire contract. Overkill at this scale; the manifest format reaches ~all
  real backends without it.
- **Compiling Dify in as a first-party provider** — rejected in favour of "one
  mechanism, no exceptions": everything is a plugin.
- **Bundling official manifests via `go:embed` as an offline fallback** — dropped
  to keep a single source of truth (the GitHub market). Manual import covers the
  offline case.
- **Ingestion cross-check to detect silent callback failures** — a post-run probe
  that queried our own `reports` table to downgrade a "succeeded" row to
  `partial` when nothing landed. Dropped: the operator wants to trust Dify's
  result. The `partial_when` seam remains if we revisit this.
- **Live-adjustable concurrency on a running job** — shrinking a running worker
  pool adds state-machine complexity for little gain; cancel-and-relaunch is the
  escape hatch, and an immutable per-job concurrency makes the job record a clean
  audit artifact.

## Consequences

- When the server cannot reach GitHub and no manifest file is on hand, no plugin
  can be added. Accepted; manual import is the escape hatch.
- If a Dify workflow's callback to `/api/reports` silently fails while Dify still
  reports success, the portal shows that row as succeeded though no report landed.
  Accepted trade-off of "trust the backend's result".
- Installing a manifest lets the server issue HTTP requests to URLs the manifest
  specifies (an SSRF surface). Mitigations: install/import is admin-only, the
  manifest's target hosts are previewed before install, and a host allowlist is
  enforced.

## Appendix — shape

Packages:

```
internal/batch/     engine (queue / pool / retries / cancel / persistence / recovery)
                    Provider interface — sole implementation: the manifest interpreter
internal/app/       tables, GitHub market fetch, manual import + validation,
                    target & job CRUD, requirePermJSON, operator role wiring
```

Provider contract (all the engine knows):

```go
type Outcome int // Ok | Partial | Failed

type RunResult struct {
    RunID  string
    Status Outcome
    Detail string
    Raw    json.RawMessage
}

type Provider interface {
    Run(ctx context.Context, inputs map[string]string) (RunResult, error)
}
```

Tables (added to `Store.init()`; no migrations, portable across sqlite/postgres):

```
plugins(id, slug UNIQUE, name, version, spec JSON, enabled, source, imported_at)
batch_targets(id, plugin_slug, name, config JSON, created_at)
batch_jobs(id, target_id, status, concurrency, max_retries,
           total, succeeded, partial, failed, created_by,
           started_at, finished_at)
batch_items(id, job_id, row_index, inputs JSON,
            status,            -- queued | running | succeeded | partial | failed
            attempts, outcome JSON, run_id, error,
            started_at, finished_at)
```

Settings key: `batch_max_concurrency`.
