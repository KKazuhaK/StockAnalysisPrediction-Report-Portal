# ADR 0019 — Additive credential hardening and report indexes

## Status

Accepted — 2026-07-16. The owner selected an additive-only rollout: no automatic data rewrite,
move, deletion, index removal, or schema-generation change.

## Context

Persistent API bearer tokens have historically been stored and returned in plaintext. A database
read therefore exposes every legacy machine credential. Browser sessions are signed and expire
after seven days, but a password change does not revoke cookies issued before the change. The
report feed also orders and collapses rows by stock and date while only single-column report
indexes exist.

## Decision

1. Add nullable `api_tokens.token_hash` and `api_tokens.token_prefix` columns. Newly created API
   tokens store only `SHA-256(token)` plus a non-secret display prefix; plaintext is returned once
   by the create endpoint. API tokens carry 192 bits of random entropy, so a fast digest is
   appropriate: an offline dictionary attack is not practical as it would be for a human password.
2. Do not rewrite existing `api_tokens.token` values. Authentication checks the digest column and
   the legacy plaintext column, so existing tokens remain valid and stay exactly where they are.
   Administrators can rotate legacy tokens deliberately when operationally convenient.
3. Add `users.session_rev`. Newly issued cookies include the revision, and every password update
   increments it atomically. Pre-revision cookies are interpreted as revision zero, so deployment
   alone does not log users out; a password change invalidates both old and revisioned cookies.
4. Add portable B-tree indexes for `(symbol, rdate, sent_at)` and `(rdate, sent_at)`. Keep the
   existing single-column report indexes. All index creation is idempotent.
5. Do not add a native full-text table. SQLite FTS5 and Postgres text search have materially
   different tokenization for CJK text; changing the existing substring-search semantics would
   produce driver-dependent results. A future full-text ADR must define one tested, consistent
   tokenizer and rebuild strategy for both drivers.

## Rollout

This change does not create a new schema generation. `ensureColumns` discovers and adds missing
plain columns from the base schema. `ensureAdditiveIndexes` runs guarded `CREATE INDEX IF NOT EXISTS`
statements after column reconciliation. Neither path issues `UPDATE`, `DELETE`, `DROP`, or copies
data between columns or tables.

The equivalent PostgreSQL rollout is:

```sql
BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS session_rev BIGINT DEFAULT 0;

ALTER TABLE api_tokens
    ADD COLUMN IF NOT EXISTS token_hash TEXT;

ALTER TABLE api_tokens
    ADD COLUMN IF NOT EXISTS token_prefix TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_api_tokens_hash
    ON api_tokens (token_hash)
    WHERE token_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_reports_symbol_date_time
    ON reports (symbol, rdate, sent_at);

CREATE INDEX IF NOT EXISTS idx_reports_date_time
    ON reports (rdate, sent_at);

COMMIT;
```

## Consequences

- Newly created tokens are not recoverable after their one-time response; an administrator creates
  a replacement if one is lost.
- Existing tokens continue to work and remain plaintext until explicitly rotated or deleted by an
  administrator. This is the intentional security tradeoff of the no-rewrite requirement.
- Deployment does not invalidate existing browser sessions. A password change signs that user out
  on every device.
- Old report indexes remain alongside the new composite indexes, using additional storage and write
  work in exchange for a strictly additive rollout.
- Full-text body search remains functionally unchanged and may still scan report bodies.
