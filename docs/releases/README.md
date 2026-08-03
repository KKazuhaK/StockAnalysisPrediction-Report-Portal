# Release notes

One file per release, the same text as the annotated git tag (`git tag -n99 <tag>`). The tag is
still where a release is cut; these exist so the notes are readable in the repo and in a diff,
which a tag message is not.

The 0.4 line is where external users, SSO and report versions landed. Read the upgrade section of
whichever release you are moving TO — the portal is pre-1.0, so per semver each `0.y` bump is a
major boundary, and a database has to reach the last release of a line before crossing one.

| Release | Date | Headline |
| --- | --- | --- |
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
