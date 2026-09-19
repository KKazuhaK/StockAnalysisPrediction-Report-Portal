# Release notes

One file per release, the same text as the annotated git tag (`git tag -n99 <tag>`). The tag is
still where a release is cut; these exist so the notes are readable in the repo and in a diff,
which a tag message is not.

That equality is enforced by `scripts/tag_release_test.sh` because it used to be false: `git tag -a
-F` strips every line beginning with `#` unless told not to, so every annotation cut before this
helper passed `--cleanup=verbatim` lost its Markdown headings — and any shell comment inside a
command example — while the file kept them. Tags already cut are left as they are; the next one
onward says what its file says.

Cutting one — the version and the commit are both optional:

```sh
scripts/tag-release.sh --next     # print the number it would use, change nothing
scripts/tag-release.sh            # derive that number, tag HEAD
git push origin v2026.38
```

Without a version, the script derives one: **the week comes from the clock** (the current UTC ISO
week) and **the revision from what this checkout has** — it cuts the highest release note written for
this week that has no tag on it yet, so the note you just wrote is the release you get. With every
note for the week already tagged, the week is continuing rather than starting and it takes the next
revision. It reads local tags and the working tree only, so a clone that has not fetched cannot see a
tag cut elsewhere; naming the version explicitly is the escape hatch, and it is validated the same
way.

The tag's message is the release note, so a number with no `docs/releases/<tag>.md` in the tagged
commit cannot be cut — the script names the file to write and stops.

**The tag must go on a commit that main CI has already passed**, and the release workflow refuses to
publish otherwise: it looks for a completed `test` run against that exact commit and requires the
whole suite, race lane included — and the race lane only runs on a push to `main`. So the note belongs
in the pull request, not in a commit made after the merge: the squash merge that lands the work is the
commit to tag, and a follow-up commit that only adds or edits the note has no run of its own and
cannot be released from.

The push is a separate, deliberate command because it is the irreversible step; the script never
pushes. It refuses a version with no note, a tag that already exists, a tag that is not CalVer, and —
the mistake worth catching — a commit that does not contain its own release note, which is how a tag
ends up describing a release the commit predates. The tag's annotation is taken from that commit, not
from the working tree.

No trailing `#` comments in that block, on purpose. zsh does not treat `#` as a comment in an
interactive shell unless `INTERACTIVE_COMMENTS` is set, so a copied line with an explanation after it
passes the `#` as the commit argument and the script dies on `fatal: Needed a single revision`.

Releases are CalVer: `vYYYY.W[.R]`, where `YYYY` is the ISO week-numbering year, `W` the UTC ISO week
the series starts in, and `R` an optional revision that starts at 1 and rises for every changed set of
artifacts. Leave the revision off for the first release of a week — `v2026.38` — and add one when a
second artifact set lands in the same week. They are different numbers, so ordering never ties. There
is no `-beta`: whether a release is a pre-release or a full release is GitHub Release metadata, set
when the draft is published, and it is what moves `:latest`. See
[ADR 0034](../adr/0034-calver-baseline-and-database-compatibility-reset.md).

The tag push prepares a **draft** release. Publish it from the GitHub UI as a pre-release or a full
release; the reconciliation workflow then updates the rolling image channels, promoting the bytes that
were already published by digest rather than rebuilding them. To re-run that by hand after a missed
event, dispatch the **Release channels** workflow (with `dry_run` to see the decision first).

A `release` event runs the workflow **from the tagged commit**, not from the default branch, so a fix
to `release-channels.yml` takes effect for releases tagged after the fix and reconciliation for an
already-cut tag keeps running the copy that tag carries. Dispatch that workflow when an existing
release needs the newer logic. If it moves a channel, `CHANNEL_LATEST_TAG` / `CHANNEL_BETA_TAG` in the
repository variables record where the channel was last put; an operator override lives in
`CHANNEL_LATEST_OVERRIDE` / `CHANNEL_BETA_OVERRIDE` beside them, and reconciliation never clears it.

## The v2026.38 database boundary

The first CalVer release reads exactly one database shape — the **v0.4.72** schema — and converts
nothing. It is not a compatible numbering cutover:

- **A database at the v0.4.72 shape starts as it always did**, with no migration and no writes.
- **Anything older is refused before any statement runs.** It must be started once by the v0.4.72
  binary first, which carries every migration the earlier lines accumulated.
- Existing release-note files are never back-filled. `v0.4.72` has no note file because the note
  convention post-dates some of the tags it was cut under; the bridge requirement for it lives here
  instead.

### Transition runbook

Rehearsed stop-writers → backup → bridge → verify → switch → rollback. `v0.4.72`'s binary and its
`ghcr.io` image tag are the bridge and must stay retrievable for as long as a rollback must be
possible.

```sh
# 1. Stop the portal so nothing is writing, and keep the old config.yaml and secret_key.
docker compose stop report-portal

# 2. Take a consistent backup with the product's own tool. A raw copy of a live SQLite file is NOT
#    adequate — it can miss the write-ahead log — and `backup` opens the database through the driver,
#    so it does not.
report-portal backup /backup/pre-cutover.jsonl        # 0600; carries password hashes

# 3. Bridge: start the database ONCE with v0.4.72. That release runs the accumulated migrations and
#    leaves the database at the shape the new one accepts.
docker run --rm -v /path/to/config:/app/config ghcr.io/kazuhahub/stockanalysisprediction-report-portal:v0.4.72
#    It is a normal start — let it come up, confirm the portal serves, then stop it.

# 4. Verify before switching: report count and a report reads back; an account signs in; an SSO
#    connection still decrypts (it needs the same secret_key); the fallback group exists.
report-portal backup /backup/post-bridge.jsonl

# 5. Switch to the new release. It verifies the shape and writes nothing.
docker compose pull && docker compose up -d
```

**Rollback** is the old binary plus the retained pre-cutover database and configuration:

```sh
docker compose stop report-portal
cp /backup/pre-cutover.jsonl /backup/rollback.jsonl
report-portal restore --force /backup/rollback.jsonl     # the v0.4.72 binary, not the new one
# start the v0.4.72 image again against that database
```

Do **not** open the post-bridge database with an older binary, and do not restore a dump taken by an
older release into the new one — restore refuses it, because the dump's columns do not cover the
current schema. The release that wrote a dump is the release that has to load it.

For a Postgres deployment, `pg_dump -Fc` and `pg_restore` are the equivalents of steps 2 and 5, and
the same ordering applies: dump before the bridge, restore before reverting the image.

| Release | Date | Headline |
| --- | --- | --- |
| [v2026.38](v2026.38.md) | 2026-09-19 | CalVer numbering, and a database compatibility reset |
| [v0.4.59](v0.4.59.md) | 2026-09-09 | An empty workflow result is not a report |
| [v0.4.50](v0.4.50.md) | 2026-09-08 | Execution modes and preset-window waiting in the queue |
| [v0.4.49](v0.4.49.md) | 2026-09-08 | Pick your own dates |
| [v0.4.48](v0.4.48.md) | 2026-09-07 | The 5日 button works, and the panel stops promising windows nothing serves |
| [v0.4.47](v0.4.47.md) | 2026-09-07 | Prices on the home cards, a minute-by-minute chart, and a source you can switch on |
| [v0.4.46](v0.4.46.md) | 2026-09-07 | The chart gets its own app, and stops being A-share only |
| [v0.4.45](v0.4.45.md) | 2026-09-07 | Live quotes, a database you can copy, and a portal you can use without a mouse |
| [v0.4.44](v0.4.44.md) | 2026-09-04 | A workflow's cache is not a report |
| [v0.4.43](v0.4.43.md) | 2026-09-02 | A report remembers what it used to say |
| [v0.4.42](v0.4.42.md) | 2026-09-02 | Reports you write yourself |
| [v0.4.41](v0.4.41.md) | 2026-09-02 | Dragging works, and works without a mouse |
| [v0.4.40](v0.4.40.md) | 2026-08-31 | Announcements stop hiding themselves |
| [v0.4.39](v0.4.39.md) | 2026-08-30 | Announcements, plural |
| [v0.4.38](v0.4.38.md) | 2026-08-26 | One prompt and one reload per deploy |
| [v0.4.37](v0.4.37.md) | 2026-08-26 | A session that ends says so |
| [v0.4.36](v0.4.36.md) | 2026-08-24 | A weekly window is a set of days, not one day at a time |
| [v0.4.35](v0.4.35.md) | 2026-08-21 | A view waits before it tells you there is nothing |
| [v0.4.34](v0.4.34.md) | 2026-08-14 | The run dialog opens on what you configured |
| [v0.4.33](v0.4.33.md) | 2026-08-11 | A workflow that asks for a file can be given one |
| [v0.4.32](v0.4.32.md) | 2026-08-11 | The panels stop pretending a phone is a desk |
| [v0.4.31](v0.4.31.md) | 2026-08-10 | Measured, then changed |
| [v0.4.30](v0.4.30.md) | 2026-08-10 | The audit log answers the questions it is asked |
| [v0.4.29](v0.4.29.md) | 2026-08-10 | A slow link stops being told things that are not true |
| [v0.4.28](v0.4.28.md) | 2026-08-10 | The footer sits on one line, not one and a bit |
| [v0.4.27](v0.4.27.md) | 2026-08-10 | The text under an exported page is the text you typed |
| [v0.4.26](v0.4.26.md) | 2026-08-10 | A button that stops repeating itself |
| [v0.4.25](v0.4.25.md) | 2026-08-10 | Things that were painted on top of each other, or off the screen |
| [v0.4.24](v0.4.24.md) | 2026-08-09 | A character that has no font stops the build, not the reader |
| [v0.4.23](v0.4.23.md) | 2026-08-08 | Exported reports print in a font somebody chose |
| [v0.4.22](v0.4.22.md) | 2026-08-05 | Confirm your identity the way you sign in |
| [v0.4.21](v0.4.21.md) | 2026-08-04 | Pull a workflow's parameters back; the compare button works |
| [v0.4.20](v0.4.20.md) | 2026-08-04 | The cleanup history records deletions, not days |
| [v0.4.19](v0.4.19.md) | 2026-08-04 | The IP database form asks only what the source needs |
| [v0.4.18](v0.4.18.md) | 2026-08-04 | The audit log records visitors, not your reverse proxy |
| [v0.4.17](v0.4.17.md) | 2026-08-04 | The IP database is a feature, not a URL box |
| [v0.4.16](v0.4.16.md) | 2026-08-04 | The IP database can fetch itself |
| [v0.4.15](v0.4.15.md) | 2026-08-04 | The audit log actually covers what it claimed to |
| [v0.4.14](v0.4.14.md) | 2026-08-03 | The account list shows activity, not just sign-ins |
| [v0.4.13](v0.4.13.md) | 2026-08-03 | The SSO page tells you which claim to map |
| [v0.4.12](v0.4.12.md) | 2026-08-03 | SAML actually signs in, and says why when it does not |
| [v0.4.11](v0.4.11.md) | 2026-08-03 | First-time SAML setup is no longer a deadlock |
| [v0.4.10](v0.4.10.md) | 2026-08-03 | The SSO guide works before you have configured SSO |
| [v0.4.9](v0.4.9.md) | 2026-08-03 | An audit log, and quotas that fit the billing cycle |
| [v0.4.8](v0.4.8.md) | 2026-08-01 | Comparing reports, and a place for assumptions to be reviewed |
| [v0.4.7](v0.4.7.md) | 2026-07-31 | The public URL moves to General |
| [v0.4.6](v0.4.6.md) | 2026-07-31 | The leftovers, closed |
| [v0.4.5](v0.4.5.md) | 2026-07-31 | Organizational units you can actually read |
| [v0.4.4](v0.4.4.md) | 2026-07-31 | Login modes, an OU tree, and the audit that followed |
| [v0.4.3](v0.4.3.md) | 2026-07-31 | The audit release |
| [v0.4.2](v0.4.2.md) | 2026-07-30 | A smaller schema behind the same behaviour |
| [v0.4.1](v0.4.1.md) | 2026-07-29 | SSO, two-factor, report versions, captcha, self-service registration |
| [v0.4.0](v0.4.0.md) | 2026-07-28 | External-user access: OU tenancy, owner-scoped reads, run quotas |
| [v0.3.10](v0.3.10.md) | | The last release of the 0.3 line |
| [v0.3.9](v0.3.9.md) | | |
| [v0.3.8](v0.3.8.md) | | |
| [v0.3.7](v0.3.7.md) | | |
| [v0.3.0](v0.3.0.md) | | |

## Upgrade paths that are NOT supported

- **Any database older than v0.4.72, opened directly by a CalVer release.** The first CalVer release
  reads only the v0.4.72 shape, so the database has to be started once by v0.4.72 first — there is no
  direct route from v0.3.x or from an earlier v0.4.x to a CalVer release.
- **v0.4.1 → anything later.** Its adoption steps were removed in v0.4.3, so the portal refuses to
  start rather than mint a second data key and silently lose every sealed secret. Recreate the
  database, or run v0.4.2 once to migrate it first, then continue up the line to v0.4.72.
- **Restoring a dump taken by a release older than the running one.** The fix is the same: take the
  database up through the release line, then back it up again.
- **Opening a post-bridge database with an old binary**, or the reverse. Each side reads one shape;
  the rollback route is the old binary with its retained pre-cutover database.

## Recovering an interrupted image publication

Release preparation requires successful full CI for the exact commit. If no push run exists
(for example on a maintenance branch), dispatch `test.yml` on that branch and wait for it.
Different release tags may build concurrently; attempts for the same tag are serialized.

Once the fixed image exists, rebuilding or replacing draft archives is refused before publication.
If the image push succeeded but uploading `release-metadata.json` failed, recover only that file:

```sh
python3 scripts/recover-release-metadata.py OWNER/REPO vYYYY.W.R
```

The recovery tool requires authenticated `gh` and Docker access. It verifies archive checksums,
compares both Linux binaries byte for byte with their image counterparts, and checks the image
revision against the annotated tag before uploading metadata. It never rebuilds or replaces an
archive or image. A mismatch requires a new version. Channel promotion requires all six named
archives, checksums, and matching metadata, and promotes the recorded immutable digest.

The Debian image index is pinned in `Dockerfile.release`; weekly Docker dependency updates keep
it moving through review. PR image builds read the main cache but do not export their own cache.
Only main pushes refresh the shared Actions image cache; release builds use the registry cache.
