# ADR 0023 — Authentication: SSO (SAML 2.0 / OIDC), TOTP 2FA, and passkeys

## Context

The portal authenticates with local passwords only: admin-created rows in `users`, bcrypt, and a
signed-cookie session (`v1|username|session_rev|exp`, HMAC over `secret_key`). ADR 0022 opened the
portal to **external** users, and external organisations expect to sign in with their own identity
provider rather than a password we issue. Both protocols are wanted — Entra ID / Okta / Keycloak /
Google all speak OIDC, while several enterprise and government IdPs still only offer SAML 2.0 — and
they must be usable **at the same time** (internal staff on one IdP, a customer on another).

This ADR covers **authentication only**: who is signing in. ADR 0022 remains the authority on
**authorization** — what they may see, run and how often. The two meet at exactly one point: an
SSO login must be able to place a user in the right **OU**, because in this codebase the OU carries
the real permissions.

Two further gaps are folded in, because they are the rest of the same story — how someone proves who
they are — and are cheaper done together than bolted on later:

- **TOTP two-factor** for local accounts, with recovery codes and step-up on sensitive actions.
- **Passkeys / WebAuthn**, as a second factor and (eventually) a password replacement.

**Password recovery already exists** (`password_reset.go`: signed-token email links, per-account rate
limiting, constant responses so account existence never leaks, async send so latency does not leak
either, fail-closed without `public_url`). It is not rebuilt here; it only gains awareness that an
SSO-sourced account has no password to reset.

A follow-on requirement is stated up front: automatic user **provisioning/sync** (SCIM 2.0) is wanted
later. This ADR therefore reserves the columns and factors the code so SCIM is an addition, not a
redesign — but builds none of it. Likewise SSO ships as **one provider**, with the row-shaped tables
and slug-addressed routes already able to carry several.

## Decision

### Libraries

| Need | Choice | Why |
| --- | --- | --- |
| SAML SP | `crewjam/saml`, **low-level `saml.ServiceProvider` only** | Matches the operator's existing panel, so its proven Entra integration and handler logic carry over. Never `samlsp.Middleware`, which ships its own JWT session cookie and would fight ours. |
| XML-DSig | `russellhaering/goxmldsig` **v1.6.0** — *explicit direct `require`* | v1.4/v1.5 carry **CVE-2026-33487** (HIGH): the reference-matching loop takes the loop variable's address without `break`, so the digest is checked against an attacker-chosen `Reference`. crewjam's own `go.mod` still pins a vulnerable version. |
| XML DOM | `beevik/etree` **v1.7.0** — *explicit direct `require`* | ≤1.6 recurses per level on write/copy; ~21 MB of nested elements is an uncatchable `fatal error: stack overflow` — one unauthenticated POST kills the binary. v1.7 adds `MaxDepth`. |
| OIDC RP | `coreos/go-oidc/v3` + `golang.org/x/oauth2` | Same as the reference panel. Discovery, JWKS rotation, ID-token verification; its algorithm allowlist excludes `none`/HS*, closing alg-confusion structurally. |
| TOTP | `pquerna/otp` | Same as the reference panel; the de-facto Go TOTP library. |
| WebAuthn | `go-webauthn/webauthn` | Same as the reference panel; the maintained successor to `duo-labs/webauthn`. |

**On the SAML library.** An evidence-based review (a `govulncheck` run on a minimal SP) preferred
`russellhaering/gosaml2`: crewjam's last commit is ~15 months old, its `go.mod` pins the vulnerable
`goxmldsig`, and it has a worse advisory history (including a Project Zero signature bypass). We take
crewjam anyway, deliberately, for consistency with the operator's other panel — but **only with the
concrete risk closed**, because the reference panel is safe today only by accident: its `goxmldsig`
reached v1.6.0 as an *indirect* dependency pulled up by an unrelated part of its much larger graph,
which a `go mod tidy` could undo. Therefore:

1. `goxmldsig` v1.6.0 and `etree` v1.7.0 are pinned on purpose rather than inherited by luck.
   `goxmldsig` is an **explicit direct require** — we import it for the signature-algorithm allowlist.
   `etree` has no direct call site, so it is an indirect requirement at our version; the pin holds
   because MVS takes the maximum, and `TestSecurityCriticalDependencyFloors` fails the build if either
   version regresses. A comment in `go.mod` cannot enforce that; a test can.
2. **`govulncheck ./...` becomes a hard CI gate**, alongside build/vet/test.
3. Only the low-level `ServiceProvider` API is used, so switching to gosaml2 later is a contained
   change if crewjam stays unmaintained.

All picks are pure Go and cross-compile under `CGO_ENABLED=0`, consistent with `modernc.org/sqlite`.

No SCIM library: when SCIM ships, hand-rolling the Entra/Okta subset on `net/http` is smaller than
the dependency.

### The account-linking rule (the single most important decision)

An external identity is linked to a local account **only** by the tuple
`(provider, issuer, subject)` — OIDC `iss` + `sub`, or the SAML IdP entity id + `NameID`, byte-exact.

**Never by email.** This is the *nOAuth* account-takeover class: Entra lets an admin of any other
tenant set an unverified `email` claim to your user's address; OIDC Core forbids keying on it; SAML
makes no verification claim about an email attribute at all. `users.email` is display and contact
only, and no auth path may `SELECT ... WHERE email = ?`. Lookups use plain `=` with bound
parameters — never `likeOp()`, which is `ILIKE` on Postgres and would make the authentication
boundary driver-dependent.

`sub` is unique only *within* an issuer, hence the composite key. Identities live in their own table
(not columns on `users`) because one human may hold both a SAML and an OIDC identity, and an
Okta→Entra migration needs both links live during the overlap.

### Group resolution assigns a role **and an OU**

The reference design this was modelled on maps IdP groups to a *role*. That is insufficient here: in
this codebase the role grants almost nothing, while the **OU** (`user_groups`) carries `restricted`,
`daily_run_quota`, `group_targets` and the report-visibility boundary (ADR 0022). A rule therefore
targets **both**, and a JIT-created external user lands directly in the correct restricted OU,
inheriting its allow-list and quota. Without this, SSO users would fall into the default group with
no entitlements — or worse, into an unrestricted one.

Rules are rows (never a JSON blob) so a future sync job can query them. Evaluation is an ordered,
first-match-wins scan; each rule has an attribute, an exact value, a target role, a target OU, a
`keep_on_miss` flag and a note. Semantics are pinned, because the reference UI leaves them ambiguous:

- No match, `keep_on_miss`, **existing** user → leave role and OU untouched.
- No match, **new** user → the provider's defaults; if no default OU is set, **DENY**. Never fall
  through to an implicit default that could be the root OU.
- `default_group` must reference a **restricted** OU, so a misconfiguration shows the user *nothing*
  rather than everything.
- A rule may target a `PermManage` role only when the provider's `allow_admin_role` flag is set;
  every such elevation is logged; SSO may never modify the built-in local `admin` account.

The evaluator is a **pure function** in a package importing neither `net/http` nor any SAML/OIDC
type: `ResolveAssignment(rules, IdentityFacts, current) → Assignment`. The SAML ACS, the OIDC
callback and a future SCIM job all build the same `IdentityFacts`, so "what role and OU do you get"
cannot drift between login and sync. It is table-testable with no HTTP, XML or network.

### Provisioning

A per-provider `provisioning` enum, **default `off`** (one enum, not two booleans, so a login can
never mint a row a future sync job refuses to own):

- `off` — an unknown subject is redirected to a static "contact your administrator" page. Nothing is
  created, no cookie is set, the subject is logged so an admin can pre-provision.
- `jit` — create with `source='jit'`, `source_ref=<slug>`, a fresh `uid`, `external_id` from the
  mapped attribute, and the provider's default OU / expiry / quota.

The response for *unknown subject*, *no rule match*, *inactive* and *expired* is **identical** —
differentiated messages are a user-enumeration oracle against the IdP. A JIT create refuses outright
to take a username that already exists as `source='local'`; that case routes to explicit admin
linking. There is no auto-linking by email, ever.

### Configuration

All SSO settings live in the **DB** and are edited in the web admin UI; `config.yaml` stays
infrastructure-only. Config is **row-shaped** (`sso_providers`), not `meta` key/value, so multiple
IdPs later are a UI change with no schema movement. The v1 UI manages one SAML row and one OIDC row.

SP/redirect URLs are **derived** from the existing `public_url` setting through one shared function —
the same value used by the metadata generator and the `Destination` comparison, because if those can
diverge you have a bypass. `public_url` already exists and deliberately has no request-derived
fallback (host-header poisoning), which is exactly the property SAML needs.

- SAML: `<public_url>/api/auth/saml/<slug>/metadata` and `…/acs`.
- OIDC: `<public_url>/api/auth/oidc/<slug>/callback`.
- A provider **cannot be enabled** while `public_url` is blank; SAML additionally requires **https**,
  because its ACS flow cookie must be `SameSite=None; Secure`.

Secrets (SP private key, OIDC client secret) are sealed with AES-256-GCM under a DEK wrapped by
`HKDF-SHA256(secret_key, salt, …)`, so rotating `secret_key` re-wraps **one row** instead of
re-encrypting every secret — see `secret_key_previous` under Consequences for the actual procedure. A blank secret on write keeps the stored one (the existing Dify
`api_key` precedent); reads return `has_secret` booleans and never a value.

### Flows

```
GET  /api/sso/providers                 public list for the login page ([] when SSO is off)
GET  /api/auth/oidc/{slug}/start        GET /api/auth/oidc/{slug}/callback
GET  /api/auth/saml/{slug}/start        POST /api/auth/saml/{slug}/acs
GET  /api/auth/saml/{slug}/metadata     SP metadata (public by design)
```

Both protocols share **one pending-login mechanism**: a server-side `auth_requests` row holding
the nonce / PKCE verifier / AuthnRequest id / return path, plus a short-lived binding cookie carrying
the same opaque token. Server-side rather than a sealed cookie because it is restart-safe, works
across the multiple production instances sharing Postgres, and gives **global single-use** via a
conditional `DELETE … WHERE token=? AND expires_at>?` requiring `RowsAffected()==1` — which a cookie
cannot. The row proves *this response answers a request we issued*; the cookie proves *and it landed
in the browser that issued it*. Both are needed; dropping the cookie is the tempting "simplification"
that removes login-CSRF protection.

Cookie policy differs by protocol and is not optional: OIDC's callback is a top-level GET
(`SameSite=Lax`), SAML's ACS is a cross-site POST (`SameSite=None; Secure`). Neither ever touches
`rp_session`, whose `SameSite=Lax` is this application's entire CSRF defence — nothing in this feature
may weaken it.

The OIDC callback's **order is the security property**: clear the cookie, handle `?error=`,
constant-time state compare, consume the row, check RFC 9207 `iss` — and only then exchange the code,
so nothing attacker-supplied reaches the token endpoint or the JWKS fetcher.

Sessions are minted through the existing `signUser` path, so account expiry (ADR 0022 R4) and
`session_rev` invalidation work unchanged.

### TOTP two-factor

Applies to **local** accounts only: for an SSO account the IdP owns the factors, and asking for a
second one we manage would be both confusing and weaker than what the IdP already enforces.

Enrolment is confirm-before-enable: generate the secret, show the QR, and only set `totp_enabled`
after the user proves one correct code — otherwise a mistyped enrolment locks the account out.
Verification allows ±1 time step for clock drift, and a used `(user, time-step)` pair is rejected so
a code cannot be replayed inside its own window. The secret is sealed with the same envelope
encryption as the SSO secrets, never returned by any API after enrolment, and never logged.

Recovery codes are generated once at enrolment, shown once, and stored **hashed** — they are
password-equivalents. Each is single-use. Consuming one is logged.

Login becomes two-step when 2FA is on: password → a short-lived, single-use *pending* token → code.
The pending token is a row in the same `auth_requests` table (it is the same problem: short-lived,
single-use, must survive a restart and work across instances), and it grants nothing on its own. The
existing login throttle covers the second step keyed by account, so the code cannot be brute-forced.

**Step-up**: changing a password, managing 2FA/passkeys, and minting API tokens re-require a factor
even inside a valid session, so a stolen cookie cannot be escalated into permanent access. The proof
travels in an `X-Step-Up-Proof` **header**, never the query string — it is a password or a live code,
and a query string lands in every proxy access log, in browser history, and in the `Referer` of any
subresource. Step-up shares the login throttle, keyed `stepup:<account>`: it is the same online
guessing oracle as the login form, the attacker merely starts from a stolen session. Enrolling a
factor is gated as well as removing one — an attacker who turns 2FA **on** with their own
authenticator locks the owner out of their own account with no self-service way back.

### Passkeys (WebAuthn)

A passkey here is a **second factor, not a passwordless login**: the ceremony requires the single-use
pending token from a completed password leg. Credentials are registered with user verification
`preferred`, so one may be possession-only, and accepting that as the sole credential would be weaker
than the password it replaced. (Reading the pending token does not consume it — a slow authenticator
must not throw the user back to the password screen; the claim happens once, at the point of no
return.) Registered per user, multiple allowed, each with a label and a last-used timestamp so an
admin or the user can see and revoke them. The Relying Party ID derives from the same `public_url` used everywhere
else — a mismatch silently breaks every credential, so it must come from the one shared function.

The credential's **sign counter is verified and stored**: a counter that goes backwards means a cloned
authenticator, which is the one thing WebAuthn's counter exists to detect. User verification is
`preferred` in v1; a passkey then counts as a second factor. Passwordless (a passkey as the *only*
factor) is left to a later change so we can require `discoverable` + `userVerification=required`
deliberately rather than by accident.

Registration and authentication ceremonies use the same pending-request table, single-use, for the
challenge — never a client-supplied one.

### What we deliberately do NOT build

Encrypted SAML assertions (a decryption-oracle surface TLS already covers on the Web SSO profile) —
and because crewjam looks for an `EncryptedAssertion` *first* and would hand it to the SP private key,
this non-goal is an explicit **refusal** in the ACS, not merely an absence of support;
implicit/hybrid flows; ROPC; refresh tokens and token storage (we need authentication, not API
access); HS*/`none`/JWE ID tokens; distributed-claims resolution; front-channel logout (it would force
`SameSite=None` on `rp_session`); RP-initiated logout in v1. Each is attack surface bought for nothing
here.

**Break-glass is guaranteed**: local password login always remains reachable, "SSO only" is never a
one-way door, `apiLogin` short-circuits on `source != 'local'` before bcrypt so SSO rows cannot be
password-attacked, and the existing `adduser` CLI is the documented recovery path.

## Schema (additive; requires sign-off per the hard rule)

All declared once in `baseSchemaStmts()` and applied by the existing reconciler. Uniqueness on
`users` is expressed as **partial unique indexes** in the index phase, because SQLite cannot
`ALTER TABLE ADD COLUMN … UNIQUE`.

Scope note: the SCIM-only columns (`uid`, `login_name`, `deactivated_at`, `deleted_at`,
`last_sync_at`) are **deliberately deferred** to the SCIM change. Because `ensureColumns` adds columns
for free on this project, "reserve everything now" buys little; only `external_id` genuinely must
ship with SSO, since it is the SSO↔SCIM join key and cannot be reconstructed after the fact. This also
removes the one non-reconciler step the earlier draft needed (`ensureUserUIDs`).

- **`users`** + `external_id`, `source` (default `local`), `source_ref`, `created_at`, `updated_at`
  — all nullable/defaulted — plus the factor columns `totp_secret_enc`, `totp_enabled`
  (default 0), `totp_confirmed_at`, and `recovery_codes` (a JSON array of **hashed**, single-use
  codes).
  `users.username` stays the primary key and becomes **formally immutable**: it is an unconstrained
  bare-string foreign key in `batch_jobs.created_by`, `chat_conversations.created_by`,
  `priority_tickets`, recurring tasks, and it is baked into every session cookie. Renaming it would
  orphan run history, chat, ticket quota and ADR 0022 report attribution.
- **The external identity lives on `users`** — `sso_provider`, `sso_issuer`, `sso_subject`, plus
  `sso_slug`, `sso_nameid_format`, `sso_attrs`, `sso_linked_at`. One account holds at most one
  identity, and `idx_users_sso_identity` (unique, partial on a non-empty subject) stops two accounts
  holding the same one.

  This began as a `user_identities` side table allowing several links per account, on the reasoning
  that an IdP migration needs both live during the overlap. That overlap is not a case this portal
  has, and the table cost a join, an index and a class of bug the columns cannot express: the side
  table's upsert wrote `username=excluded.username`, so a second account signing in with an existing
  subject silently took the link and locked the first out of SSO. On the users row the same attempt
  is a unique-index violation at the moment it happens. Matching the Passwall panel, which stores
  the identity as two columns on its user row, decided the shape; the failure mode decided that it
  was the right call.
- **`sso_providers`** — one row per IdP: kind/slug/enabled/provisioning, defaults, SAML metadata +
  certs, SP keypair, OIDC issuer/client/scopes/discovery cache, attribute mapping.
- **The group rules live in `meta`** — one ordered JSON list under `sso_group_rules` (`ord`, `attr`,
  `value`, `target_role`, `target_group`, `keep_on_miss`, `note`). They were a table and it earned
  nothing: every write already replaced the whole list in a transaction, because an admin edits it as
  one ordered thing, so the rows only ever moved together. One value is written with one statement
  and cannot end up half-applied. A rule may be **global** (pinned to no provider), which is why the
  list belongs to the portal in `meta` rather than to a column on `sso_providers` — the Passwall
  panel, which has no global rules, keeps its role rules as a JSON column on the provider config.
- **`webauthn_credentials`** — one row per passkey: credential id (unique index), the serialized
  credential, owning username, label, sign counter, created/last-used.
- **`auth_requests`** — short-lived single-use state, TTL ~10 min. Shared by **all four** flows —
  SAML, OIDC, the 2FA pending-login step and WebAuthn challenges — because they are the same problem
  (single-use, restart-safe, cross-instance). One table, one sweeper, one conditional-DELETE
  consumption rule.
- **`sso_assertion_seen`** — SAML replay cache, keyed on `sha256(idp_entity_id ‖ assertion_id)` so one
  IdP cannot poison another's ID space.
- **The keyring lives in `meta`** — `keyring_salt` and `keyring_wrapped_dek`. It began as a
  single-row table on the instinct that key material deserves its own place; one salt and one
  wrapped key are two settings, which is the shape every other single value already uses.

These three moves — the keyring, the group rules, and renaming `sso_auth_requests` to
`auth_requests` once TOTP, WebAuthn and email verification all parked their pending state in it —
each shipped with a copy-then-drop adoption step for a database that had run v0.4.0 or v0.4.1. Those
steps are **gone as of v0.4.3**. SSO first shipped in v0.4.1, the only line that ever had the old
tables, and no deployment ever ran it: production went v0.3.10 → v0.4.3 directly, and a v0.3.x
database has none of these tables, so every adoption step was dead code that could not fire. A
database that did run v0.4.1 with SSO configured cannot be upgraded in place — recreate it.

The two TTL tables are swept by `authSweepLoop`, its **own** always-on 15-minute tick. Riding on the
storage-retention pass (ADR 0017) was the original plan and was wrong: that pass only runs when an
admin has configured retention, so in the shipped default configuration both tables would grow
forever. Expiring ephemeral rows is hygiene nobody opts into.
`ensureUserUIDs()` fills `uid` for pre-existing rows at init: idempotent, self-healing, moves and
reshapes nothing — the same class as the existing `EnsureDefaultGroup()`.

## Security requirements

Full checklist in the implementation plan; the items that most commonly go wrong:

1. **gosaml2 returns `(info, nil)` for an expired or wrong-audience assertion** — hard-fail on
   `WarningInfo.InvalidTime` / `NotInAudience` immediately after every `RetrieveAssertionInfo`.
2. **`Destination` must be present** and equal the derived ACS URL — gosaml2 skips the check when the
   attribute is absent, so absence must be our own rejection.
3. **`InResponseTo` is parsed but never compared by gosaml2** — bind both `Response/@InResponseTo` and
   `SubjectConfirmationData/@InResponseTo` to the stored request id. IdP-initiated is opt-in, default
   off, and when off an absent `InResponseTo` is a rejection, not a skipped check.
4. **The nonce is populated but never checked by go-oidc** — compare it ourselves, "must be present
   AND must match" (the `if n != "" && n != want` form passes on an absent nonce).
5. Extract claims **only** from the element the signature verifier returns, never the bytes off the
   wire; keep the XML round-trip validator in the path; reject DTDs.
6. Enforce our own signature-algorithm **allowlist** (SHA-256/384/512, RSA or ECDSA) over every
   `SignatureMethod` and `DigestMethod` in the document, before parsing. goxmldsig maps `rsa-sha1`
   straight to `x509.SHA1WithRSA` and applies no policy, and an unknown algorithm must be refused
   rather than accepted by omission. Checking every occurrence, not just the outermost, is the point:
   a strong response signature must not vouch for a SHA-1 assertion signature.
7. Reject multiple assertions, duplicate attribute names (attribute pollution aimed at a tenancy
   boundary), and transient NameID as a link key.
8. **SSRF-guard every outbound fetch** (metadata, discovery, JWKS, token, userinfo) with a dial-time
   IP re-check so DNS rebinding cannot win. The codebase has no address filtering today.
9. **Fail closed on metadata refresh**: never blank the cert store on a failed fetch; keep
   last-known-good, mark stale, and show the cert-set diff so a swapped trust anchor is visible.
10. Bump `session_rev` whenever an SSO user is deactivated, unlinked, or has their role or OU changed.

## Rollout — delivered

All phases are implemented on `feat/auth-sso`, each with tests written first:

| | What landed |
| --- | --- |
| P0 | Schema (9 `users` columns, 7 tables), envelope crypto, expiry sweep, `govulncheck` as a CI gate |
| P1 | `internal/ssorules` (dependency closure: `strings`), identity linking, JIT username sanitizer, federated accounts refused before bcrypt |
| P2 | SSRF-guarded fetching, the single-use pending-request store, OIDC end to end |
| P3 | SAML end to end |
| P4 | SSO admin API + admin page |
| P5 | TOTP 2FA |
| P6 | Passkeys |
| P7 | Adversarial review, live verification |

Two ordering notes worth keeping: OIDC came before SAML because its verification surface is smaller
and its footguns fewer, so the shared `completeSSOLogin` tail was exercised by the easier protocol
first. 2FA and passkeys came after SSO because they reuse its envelope crypto, its pending-request
table and its step-up plumbing — building them first would have meant building those twice.

The user-facing half shipped with it, because a factor nobody can reach protects nobody: an
`/account` page (password change, TOTP enrolment with its one-time recovery codes, passkey
registration and revocation) and the passkey button on the second-factor step of sign-in. Both were
missing in the first cut — the endpoints existed, and enrolling was an admin errand.

**Still deferred, deliberately:** multi-provider admin UI (the tables and routes already carry a
slug, so it is a UI change with no schema movement) and SCIM 2.0 (its own ADR; `external_id`,
`source` and `source_ref` are already recorded so accounts will join up when it arrives).

### What live verification actually proved

Beyond the unit tests, against a running server: with SSO off the public list is empty and every
`/api/auth/*` route 404s; enabling a provider without a public URL, or SAML over plain http, is
refused with a reason; saving a SAML provider mints its SP keypair and derives the entity id and ACS
URL; a metadata fetch aimed at `169.254.169.254` is refused by the SSRF guard, and a failed fetch
leaves the previous document in place; no admin response contains a private key or a sealed secret;
and 2FA behaves end to end — the password leg issues no session, a recovery code completes the login
exactly once, and a consumed pending token cannot be replayed.

## Consequences

- **Nothing changes until an admin enables a provider.** With no rows, `/api/auth/*` 404s and password
  login is untouched.
- **SAML requires an https `public_url`.** Accepted: the ACS flow cookie must be `SameSite=None;
  Secure`, and the alternative — dropping the browser binding — is strictly worse.
- **Rotating `secret_key` takes one extra boot.** Set the old key as `secret_key_previous` (or
  `RP_SECRET_KEY_PREVIOUS`) beside the new one and restart: the keyring is re-wrapped in place, the
  data key is unchanged, and no stored secret is re-encrypted or re-entered. Remove the setting
  afterwards — the portal logs a line on every boot while it is still there. Rotating **without** it
  fails loudly and changes nothing, naming both remedies (restore the old key, or delete the two
  `meta` keyring rows and re-enter the SSO secrets); it deliberately does not mint a fresh data key,
  which would silently orphan everything already sealed.
- **Deprovisioning lags until SCIM.** An IdP-side disable is invisible to us for the session lifetime;
  admin-side disable is already instant. Mitigated by a shorter session for SSO users (`session_hours`),
  which is stamped into the **signed token**, not only the cookie's `MaxAge` — `MaxAge` is a hint the
  holder of a stolen cookie simply ignores, so a limit expressed only there would not limit the one
  thing it exists for.
- **Per-provider SAML clock skew is not applied.** crewjam exposes `MaxClockSkew` only as a package
  global, so writing it per request would race concurrent logins and leak one provider's tolerance into
  another's verification. The column stays, unused, until upstream makes it per-SP; the library default
  is used. The assertion replay entry is sized to `NotOnOrAfter + MaxClockSkew` for the same reason —
  an entry that lapsed before the acceptance window closed would reopen the replay it exists to close.
- We take on an XML signature verifier. That is why the library choice is evidence-based, why
  `govulncheck` becomes a gate, and why the verification checklist is tested first.
