# ADR 0027 — Backup and restore: one logical dump, both drivers

## Context

Everything the portal owns lives in the database. Accounts, groups and the OU tree, report bodies,
the hand-written versions and their revision history, installed apps and their files, API tokens,
webhooks, recurring tasks, the sealed SSO keyring, and every setting that is not one of the six
lines in `config.yaml` — all of it is rows.

There was no way to take a copy of it and no way to put one back. Not a command, not an endpoint,
not a paragraph in the README. The consequences were not theoretical:

- The Docker stack keeps Postgres in a named volume, and `docker compose down -v` — the documented
  way to reset a stack, and the first thing anyone tries when a container misbehaves — deletes it.
- The release image carries the portal binary and nothing else. There is no `pg_dump` inside it, so
  even an operator who knew to reach for one could not, without installing a Postgres client
  somewhere and learning the DSN the compose file assembles.
- A SQLite deployment is a single file, which *looks* like it needs no backup story, right up to the
  point where someone wants to move to Postgres and finds there is no path between them.

The `secret_key` rotation work in the same release made this sharper. That failure — SSO down, no
page able to repair it — has a documented remedy that involves deleting two rows from `meta`. Any
remedy of that shape is only reasonable if a copy exists first.

## Decision

### Two CLI subcommands, `backup` and `restore`, over one format

Not a wrapper around `pg_dump` and SQLite's `.dump`, and not an HTTP endpoint.

**One format for both drivers**, because the alternative is two runbooks and a fork in every
sentence of the documentation. The same command works whichever driver a deployment runs, the image
needs no client binaries, and — the part that turned out to matter most — a dump is portable
*between* drivers. "We outgrew SQLite" and "we want to go back to a single file" both become a
backup and a restore rather than a migration project.

**A CLI subcommand rather than an endpoint**, because a restore replaces the whole database while
the process that would serve the request is reading it. The existing `adduser` / `recompute-kinds` /
`freeze-names` commands already establish the shape: open the store, do the thing, exit.

### JSON Lines: a header, then per table a section and its rows

```
{"format":"report-portal-backup","version":1,"created_at":…,"driver":…,"app_version":…,"tables":[…]}
{"table":"reports","columns":["id","title",…]}
{"row":[1152921504606846976,"深度分析 · 一季度",…]}
```

Line-oriented so a dump streams in constant memory in both directions and stays greppable. `-` means
stdin/stdout, so `backup - | gzip > x.gz` and `zcat x.gz | restore - --force` compose with the tools
an operator already has, and compression stays their choice rather than ours.

Three details are load-bearing rather than incidental:

- **Numbers are decoded as `json.Number`, never through `float64`.** A report id past 2^53 would
  otherwise come back as a *different* id — silently, and only for the largest rows.
- **Binary is tagged (`{"$b64": …}`) and the tag is decided by the column's declared type**, not by
  the Go type that came back from the driver. Both drivers return `[]byte` for real binary, but a
  text column that ever arrives as `[]byte` must go back as text, or restoring into SQLite turns a
  string into a blob.
- **The table list comes from `baseSchemaStmts()`**, the same declaration `ensureColumns` reads. A
  new table is in the backup the day it is declared; nobody has to remember a second list. A test
  asserts the walk covers every declared table, because a backup that silently omits one is worse
  than no backup at all.

### The dump is one read transaction; the restore is one write transaction

A table-by-table read of a live database is a sample of a moving target, not a snapshot. The dump
runs inside a read transaction (repeatable-read on Postgres), so what comes out existed together.

The restore empties **every table in the schema** — not only the ones the dump carries — and loads
inside a single transaction. A table the dump omits must end up empty too; anything else produces a
mix of two databases that never existed. A dump cut short by an interrupted download or a full disk
therefore leaves the target byte-identical, which is the only acceptable outcome when the
alternative is a database holding the first third of someone else's data with its own already gone.

### Without `--force`, `restore` is a dry run

The default form parses and validates the entire file, reports what it read and exactly how many
existing rows it would delete, and writes nothing.

That is two things at once. It makes "is this backup actually readable?" a question an operator can
answer on a live system without a spare database — the check people otherwise never run until the
day it matters. And it makes the destructive form explicit rather than something learned afterwards:
there is no arrangement of arguments that replaces a database by accident.

### A column this build has no schema for is a hard error

The cost of a logical dump is that it restores into the schema the running binary creates, so a dump
written by a **newer** build can carry a table or a column this one has never heard of. That is
refused, naming the table and column and the version that wrote the file, rather than dropped.
Silently discarding a column is the one way this design could lose data without anyone noticing, so
it is the one case that stops the world.

The reverse — an older dump, a newer schema — loads fine: the columns the dump lacks take their
declared defaults, and the restore reports which ones those were rather than leaving it to be
discovered. This is the ordinary upgrade path (restore last night's backup after upgrading), so it
has to work without ceremony.

### Postgres identity columns

`pkAuto()` is `BIGINT GENERATED ALWAYS AS IDENTITY` on Postgres, which **rejects** an explicit id
unless the insert says `OVERRIDING SYSTEM VALUE` — and ids must survive, because half the database
refers to reports and groups by them. The sequence behind such a column does not move when a value
is supplied either, so it is reset past the restored maximum inside the same transaction; without
that, the first report written after a restore collides with one that was just loaded.

Neither behaviour exists on SQLite, whose `AUTOINCREMENT` accepts explicit ids and advances itself.
So the SQLite tests prove nothing at all about the driver production actually runs, and the Postgres
path is covered by its own integration tests behind `TEST_POSTGRES_DSN`.

## Consequences

- **A dump is a secret.** It carries password hashes, API token hashes (and any legacy plaintext
  token), and the wrapped keyring. Files are created `0600`. The SSO secrets inside it are sealed
  under a data key that is itself wrapped under `secret_key`, which is in `config.yaml` and *not* in
  the dump — so a stolen backup does not yield them. That also means **a backup without a copy of
  `config.yaml` cannot restore working SSO**, and the README says so beside the command.
- **A backup pauses writes on SQLite, briefly.** The dump is one read transaction, and SQLite's pool
  here is a single connection, so nothing else writes while it runs. Measured: **1.2 seconds for
  50,000 reports** with realistic bodies (~41k rows/s). That is a pause, not an outage, and it buys
  the snapshot — a table-by-table read of a moving database is not a backup of anything. Postgres has
  no such pause: its dump runs at repeatable-read alongside everything else.

- **A dump is about the size of your report bodies.** 50,000 reports came to 307 MiB uncompressed —
  the format adds almost nothing over the text itself, which is the intent, but it does mean an
  operator writing straight to a file gets a large one. `backup - | gzip` is the reason `-` exists,
  and prose compresses well.

- **`restore` is offline.** It must run with the portal stopped. Nothing enforces that — it would
  mean a lock the CLI does not otherwise have — so it is stated in the output of every dry run.
- **It is not a point-in-time recovery system.** One file, one moment. Deployments that need more
  than that should still take filesystem or `pg_dump` backups underneath; this exists so that every
  deployment has *something*, and so the something is one command.
- **Ephemeral tables are included** (`auth_requests`, `sso_assertion_seen`). They are purged by
  expiry anyway, and a backup that decides on the operator's behalf which rows do not matter is a
  backup nobody can reason about.
- **Restoring across a major schema boundary is refused, by its own check.** An earlier draft of this
  ADR claimed `requireSchemaBaseline` (ADR 0013) already covered it. It does not, and cannot:
  that guard runs at open time against the **target** database — which at that moment is empty or
  current-generation — and never sees the dump. Without a check of its own, a cross-generation
  restore "succeeded", wrote the dump's `schema_version` into `meta`, and only failed on the *next*
  boot: a delayed failure after a destructive operation, in the worst possible order.

  So the dump records its generation **in the header**, not among the `meta` rows (which sort into the
  middle of the file), and the restore refuses a mismatch up front, naming which way it is wrong and
  the remedy — restore with the release that wrote it and let that release upgrade, or upgrade the
  portal first. A dump with no recorded generation is allowed rather than rejected over a question it
  cannot answer.

  The generation is read on the dump's own transaction rather than through the pool. On SQLite the
  pool is one connection wide and the dump's transaction is holding it, so the obvious
  `Store.schemaVersion()` call deadlocks — which is what the first version of this did, caught by the
  test rather than in production.
