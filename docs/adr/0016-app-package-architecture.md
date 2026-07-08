# 16. `internal/app` package architecture: one package now, Store/Server decomposition later

## Status

Accepted — 2026-07-08. Records a standing organizational decision for `internal/app` and the
trigger/shape for the future `Store`/`Server` decomposition. Relates to ADR 0013 (the last time
the package's shape was reworked at scale).

## Context

`internal/app` holds ~39 source files (~11.7k lines) plus ~52 test files, and the question came
up whether that many files in one folder meets a big-company / large-open-source bar.

The measured facts:

- The reusable **domain logic is already extracted** into sibling packages — `internal/batch`,
  `internal/queue`, `internal/dify`, `internal/webhook`, `internal/mail`, `internal/legacy`,
  `internal/config`, `internal/version`, `internal/web`. What remains in `internal/app` is the
  HTTP-handler + persistence + wiring layer.
- Two central types dominate it: **`Store` (~178 methods, `store.go` 1344 lines)** and
  **`Server` (~215 methods, `server.go` 1146 lines)**.
- Files are already grouped by a naming convention: prefix by subsystem (`batch_*`, `chat_*`,
  `apps_*`, `webhook_*`, `user_*`, `dify_*`) and suffix by role (`*_api.go` handlers,
  `*_store.go` persistence, `*_run.go` orchestration).

Two Go-language constraints shape the options:

1. **A type's methods must live in its own package.** So `batch_store.go`'s 50 `*Store` methods,
   chat's 7, user's 18 cannot move into `internal/app/<sub>/` without moving/splitting `Store`
   itself. Likewise the ~215 `*Server` methods.
2. **A single package spread over many files is idiomatic Go**, not a smell — `net/http` and
   `runtime` in the standard library are each one package of dozens-to-100+ files. Go's actual
   anti-pattern is over-splitting into many tiny packages with awkward exported APIs and
   import-cycle gymnastics.

Splitting the subsystems into sub-packages would therefore require: per-subsystem interfaces for
their `Store` needs (else an `app ↔ sub` import cycle), extracting the shared unexported HTTP
helpers (`writeJSON` ×20, `jsonError` ×32, `pathID`, `readJSON`, `okJSON`) into a shared package,
and moving the `*Server`-bound auth middleware — i.e. a **re-architecture of `Store` and
`Server`, not a file move.**

## Decision

1. **`internal/app` stays one package.** The file count is not a Go quality problem; splitting
   for tidiness alone would be *less* idiomatic and high-risk. Organization is by the existing
   name-prefix + role-suffix convention.

2. **`doc.go` is the package's map.** A package doc comment lists every source file grouped by
   subsystem, so `go doc ./internal/app` (or opening `doc.go`) navigates the folder without a
   re-architecture. New files join a documented group; the map is kept current.

3. **Static analysis is a CI gate.** `staticcheck` runs in CI alongside `go build` / `go vet` /
   `go test` (the tree passes it clean today). This is the objective standards bar for the whole
   module, and it guards the large types against dead code / real bugs as they grow.

4. **The `Store`/`Server` god-objects are the real quality lever, and their decomposition is a
   deliberate, *triggered* future refactor — not this cleanup.**
   - **Triggers to revisit** (do it when a trigger fires, not preemptively): a subsystem needs to
     be unit-tested in isolation from the rest of `Server`; a new subsystem would push a central
     type materially larger; or the coupling is repeatedly causing merge pain / accidental
     cross-talk.
   - **Target shape:** `Store` → per-domain repositories (e.g. a `store` package exposing grouped
     types, or `BatchStore` / `ChatStore` / … splitting the one type), reached through interfaces
     the subsystems declare for what they use. `Server` → a thin core that composes cohesive
     sub-handlers, each owning its own in-memory state (the chat live-registry, the batch
     scheduler) instead of it all living on one struct.
   - **Approach:** incremental, one subsystem at a time, each landing green (build + tests +
     staticcheck) and behind its own PR/ADR; **not** mid-feature on a shared branch. Start with
     the most self-contained subsystem (`apps`, ADR 0003) as the pattern-proving POC before
     committing to the rest; do `batch` last (50 store methods + the scheduler on `Server`).

## Consequences

- **Navigable without churn:** `doc.go` + `go doc` make the 39 files legible; no git-history
  noise from mass renames, no conflict with in-flight feature work.
- **Objective bar raised:** CI now enforces `staticcheck` module-wide; a regression in dead
  code / simplifiable constructs / a class of real bugs fails the build, not just review.
- **The god-objects remain, by choice** — an accepted trade-off for a single-binary app, now with
  a documented decomposition path and explicit triggers rather than tribal knowledge, so the work
  is scoped and deferred honestly instead of bolted onto unrelated changes.
- **Revisit** when a trigger above fires; until then, keeping the domain logic in its sibling
  packages (already the case) is what keeps `internal/app`'s size from being a coupling problem.
