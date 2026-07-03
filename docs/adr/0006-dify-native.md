# 6. Dify-native batch targets (typed client + probe config)

## Status

Proposed — 2026-07-03. Client implemented (`internal/dify`); target/API/UI rework
pending. Supersedes the generic-plugin framing for batch (ADR 0001) now that the
project is Dify-specific.

## Context

The batch feature (ADR 0001) was built backend-agnostic: a JSON "manifest" declares
the request/response template and the input fields; a generic interpreter runs it.
That flexibility is unused — the project is now **only** Dify. Meanwhile the admin
flow (import a manifest, understand its shape) is too fiddly. We want the simplest
possible "which workflow do I run" config.

Key facts about Dify's workflow-app service API (verified against the live instance,
v1.15.0):

- **Which workflow = which API key.** Each Dify app has its own service key
  (`app-…`); `POST /v1/workflows/run` with that key runs *that* workflow — no
  workflow id is passed. `base_url` is per Dify instance.
- The workflow's inputs are discoverable: `GET /v1/parameters` returns
  `user_input_form` — an array of `{ "<type>": { variable, label, required, … } }`
  where `variable` is the input key (and our batch CSV column). `GET /v1/info`
  returns the app name.

## Decision

**A batch target is a Dify workflow, configured by pasting its API key; the portal
auto-discovers the name and inputs. No manifest.**

- **`internal/dify` typed client** (done): `Info()`, `Parameters() → []Input`,
  `RunWorkflow(inputs) (blocking)`. Replaces the manifest interpreter for Dify.
- **Config flow**: the new-target form takes `name` (friendly) + `base_url` +
  `api_key`, plus a **Probe** button → `POST /api/admin/batch/dify/probe` calls
  `/info` + `/parameters` → returns the workflow name + inputs → admin confirms →
  save. `base_url` is per-target (multiple Dify instances allowed).
- **Inputs move onto the target.** Today inputs live on the plugin; each Dify
  workflow has its own inputs, so a target stores `base_url`, `api_key` (secret),
  and the discovered `inputs[]`. The batch CSV columns come from the target.
- **Manual fallback.** If probe fails (e.g. a Dify that can't reach `/parameters`),
  the admin types the input variable names by hand — the target still works.
- **The generic plugin/manifest stays as a hidden "advanced" path**, not the
  default. The engine (ADR 0001) and queue (ADR 0004) are unchanged; only the
  Provider construction switches to the Dify client for Dify targets.

## Consequences

- `buildProvider` returns a Dify-client-backed provider for Dify targets; the batch
  admin page loses the "plugin" concept (a target is just a workflow).
- New: `dify/probe` endpoint; targets store `inputs` (JSON) alongside config.
- The run path uses `RunWorkflow` (and can later add `/workflows/tasks/:id/stop` for
  true cancel — ADR 0004 phase 3).
- Verified input shape from the live workflow "1-6-4投资决策模块": one input
  `symbol` (上市公司代码, required, text-input).

## Note (ops)

The live Dify's service API was returning 500 on all read endpoints because the
workspace had **two `owner` accounts** (Dify's auth does `.one_or_none()` on the
tenant owner). Fixed by demoting one to `admin` (one owner required). Unrelated to
the portal, but it blocked probing until fixed.
