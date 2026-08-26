# Release notes

One file per release, the same text as the annotated git tag (`git tag -n99 <tag>`). The tag is
still where a release is cut; these exist so the notes are readable in the repo and in a diff,
which a tag message is not.

The 0.4 line is where external users, SSO and report versions landed. Read the upgrade section of
whichever release you are moving TO — the portal is pre-1.0, so per semver each `0.y` bump is a
major boundary, and a database has to reach the last release of a line before crossing one.

| Release | Date | Headline |
| --- | --- | --- |
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

- **v0.4.1 → anything later.** Its adoption steps were removed in v0.4.3, so the portal refuses to
  start rather than mint a second data key and silently lose every sealed secret. Recreate the
  database, or run v0.4.2 once to migrate it first.
- **Skipping a 0.y boundary.** A v0.3.x database must reach v0.3.10 before moving to the 0.4 line.
