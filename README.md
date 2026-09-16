# Research Report Portal

[English](README.md) | [Chinese (Simplified)](README.zh-CN.md)

A self-hosted research report portal that replaces the legacy Mail Research Report System. The frontend is built with React, Ant Design, and Vite; the backend is a single Go binary that serves a JSON API and embeds the built SPA with `go:embed`. SQLite and PostgreSQL are supported, and Docker deployment is included.

## Features

- **Unified search**: search by stock symbol or name with autocomplete, report counts, and the latest report date. Advanced filters cover report type, date range, keywords, source, and sorting.
- **Stock timelines**: aggregate every report for a stock, select a date, then browse categories, report types, and full report content.
- **Self-contained history**: legacy reports were imported into the local store, so new and historical reports share one source of truth. The retired portal is no longer required at runtime.
- **Live quotes and daily charts**: show current price, change, OHLC, volume, amount, quote time, and hand-rendered SVG daily charts for 1 month, 3 months, 6 months, and 1 year. Tencent is the primary source and Sina is the fallback. Data is fetched on read with TTL caching and single-flight deduplication; it is not persisted or polled in the background. See [ADR 0028](docs/adr/0028-live-quotes.md).
- **Markdown and HTML rendering**: render Markdown with `react-markdown` and GFM support, with a direct HTML fallback for legacy reports.
- **Exports**: export reports as Markdown or PDF. PDF generation uses `wkhtmltopdf` in the release image.
- **Web administration**: manage entry buttons, report types, accounts, roles, tokens, and API documentation. Entry buttons and report types support drag-and-drop ordering.
- **Multiple API tokens**: manage Dify Bearer tokens with notes, scopes (`all`, `ingest`, or `query`), and expiration dates.
- **Accounts and roles**: use an extensible role registry. The first startup creates an `admin` account and prints its generated password to the terminal. Accounts can expire and invalidate their active sessions.
- **Report versions**: keep multiple versions of a report, such as an internal full version, an external conclusion-only version, or a customer version. Each version has independent read permissions and visibility rules. See [ADR 0024](docs/adr/0024-report-versions.md).
- **Single sign-on**: support SAML 2.0 and OIDC/OAuth2 at the same time, with encrypted secret storage, ordered group mapping, role assignment, and organizational-unit boundaries. See [ADR 0023](docs/adr/0023-sso-saml-oidc.md).
- **Two-factor authentication and passkeys**: local accounts can enable TOTP and register multiple WebAuthn passkeys. Password changes, passkey changes, and 2FA changes require re-authentication and follow the same lockout policy as login.
- **Themes and localization**: support light, dark, and system themes, plus Chinese and English UI locales. The report body remains source data and is not translated.
- **Zero-configuration startup**: generate `config.yaml` and a random `secret_key` on first startup. Infrastructure settings stay in the config file; accounts, report types, tokens, webhooks, apps, and other product settings are managed in the web UI and stored in the database.

## Docker deployment

~~~bash
mkdir -p /opt/StockAnalysisPrediction-Report-Portal
cd /opt/StockAnalysisPrediction-Report-Portal
curl -O https://raw.githubusercontent.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/main/docker-compose.yml
docker compose up -d
docker compose logs            # prints the generated admin password on first startup
~~~

Open `http://<host>:8790` in a browser. The compose file binds to `127.0.0.1:8790` by default; use a reverse proxy and TLS for external access. Sign in with the generated password and change it in account management.

To update an installation:

~~~bash
docker compose pull && docker compose up -d
~~~

The image tags are `:latest` for the stable release, `:beta` for the latest prerelease, and `:vX.Y.Z` for a pinned version. The first startup creates `./config/config.yaml`; normally only `secret_key` needs to be set manually. Generate one with `openssl rand -hex 32`.

## Configuration

`config.yaml` contains infrastructure settings only. Accounts, entry buttons, report types, tokens, webhooks, apps, and other product settings are managed in the web UI.

~~~yaml
listen: ":8790"
secret_key: "a-long-random-secret"  # session signing and deployment secret
db_driver: "sqlite"                 # sqlite (default) or postgres
db_path: "data/portal.db"
# db_driver: "postgres"
# db_dsn: "postgres://user:pass@127.0.0.1:5432/reports?sslmode=disable"
~~~

### Backup and restore

The database contains the portal's accounts, groups, report bodies, manually authored reports, revision history, apps, tokens, webhooks, and settings.

~~~bash
report-portal backup dump.jsonl          # export the complete database
report-portal backup - | gzip > dump.gz  # stream to gzip

report-portal restore dump.jsonl         # dry run: validate and report counts
report-portal restore dump.jsonl --force # replace the database; stop the portal first
~~~

The same format works with SQLite and PostgreSQL, so moving between the two drivers requires one export and one restore. Restore is replacement rather than merge, and the operation is transactional. Without `--force`, restore writes nothing.

For Docker:

~~~bash
docker compose exec report-portal /app/report-portal backup - | gzip > dump-$(date +%F).gz
zcat dump-2026-09-04.gz | docker compose exec -T report-portal /app/report-portal restore - --force
~~~

Backups contain password hashes and token hashes, so the files are secrets and are written with mode `0600`. The SSO keyring is encrypted in the database, but its wrapping key is derived from `secret_key` in `config.yaml`; keep the config directory with the backup.

See [ADR 0027](docs/adr/0027-backup-and-restore.md) for the design and operational details.

### Rotating `secret_key`

The `secret_key` signs sessions and protects the SSO keyring. To rotate it, configure both keys for one restart:

~~~yaml
secret_key: "the-new-long-random-secret"
secret_key_previous: "the-old-secret"  # or RP_SECRET_KEY_PREVIOUS
~~~

The application re-wraps the existing data-encryption key, then logs that `secret_key_previous` can be removed. Rotating the key invalidates all active sessions. If the old key is permanently lost, the stored keyring must be removed and all SSO secrets entered again.

### PostgreSQL

SQLite is suitable for small deployments and requires no separate service. For multiple instances, larger installations, or a shared database with Dify, set `db_driver: postgres` and `db_dsn`; the application code is the same.
The PostgreSQL path is covered by integration tests.

## Dify ingestion API

Dify workflows can ingest reports with `POST /api/v1/reports` using `Authorization: Bearer <token>`. Create a token with the `ingest` scope under system settings. The complete API documentation is available in the web UI, and the machine-readable OpenAPI specification is served at `/api/openapi.json`.

Example request:

~~~json
{
  "symbol": "002594",
  "name": "比亚迪",
  "date": "2024-01-01",
  "kind": "投资决策",
  "subtype": "汇总",
  "title": "比亚迪 投资研究与决策报告 V3.15.6",
  "version": "V3.15.6",
  "body_md": "# 结论\n**买入**。",
  "run_id": "batch-2024-01",
  "source": "dify/1-6-4投资决策/V3.15.6",
  "tracking": [
    { "itype": "assumption", "content": "毛利率维持 20%", "status": "pending", "review_point": "下季度财报" }
  ]
}
~~~

Required fields are `date`, `subtype`, and at least one of `symbol` or `title`. A report must provide non-empty `body_md` or legacy-compatible `body_html`; Markdown takes precedence when both are present.

The report identity key is `symbol|date|subtype|title|version`. Ingesting the same key overwrites the existing report; `run_id` is only a batch label. The title always participates in identity, and omitting `version` selects the default report version. Producer workflows should use the stable `dify/<module>/<execution-version>` form for `source`; the Portal recognizes a trailing `V...` and displays it as the execution version. This execution version is independent of the report-edition field `version` used in the identity key.

## Local development

For frontend and backend development with hot reload:

~~~bash
# 1. Backend: JSON API on :8790
cp config.example.yaml config.yaml           # set secret_key; leave accounts empty for first-run setup
go run ./cmd/report-portal                    # prints the generated admin password

# 2. Frontend: Vite dev server on :5173, proxying API requests to :8790
cd web && npm install && npm run dev
~~~

Open `http://localhost:5173`. Run frontend type checking with `npm run typecheck`.

To verify the embedded SPA:

~~~bash
cd web && npm run build              # writes internal/web/dist/
go run ./cmd/report-portal           # serves the embedded SPA on :8790
~~~

Useful commands include:

~~~bash
go run ./cmd/report-portal hashpw 'password'
go run ./cmd/report-portal adduser <name> <password> admin
go run ./cmd/report-portal fetchnames
go run ./cmd/report-portal backup <file|->
go run ./cmd/report-portal restore <file|-> [--force]
go run ./cmd/report-portal recompute-kinds
go run ./cmd/report-portal freeze-names
go run ./cmd/report-portal version
~~~

## Release process

Push a `v*` tag to trigger CI:

~~~bash
git tag v1.0.0
git push origin v1.0.0
~~~

The release workflow cross-compiles six platforms, publishes GitHub Release archives with SHA256 checksums, and pushes multi-architecture images to `ghcr.io`. Tags with a hyphen, such as `v1.0.0-beta`, are prereleases: they update `:beta` only and leave `:latest` unchanged.

After the first image push, set the GitHub Container Registry package to public if unauthenticated `docker compose pull` is required.

## Extension points

- **Roles**: add an entry to the `roleRegistry` in `roles.go`; account management and authorization pick it up automatically.
- **Localization**: maintain locale resources in `web/src/locales/*.json`; components use `useTranslation()` and `t('key')`.
- **Report types**: report types are discovered from data and managed in the web UI, including grouping, ordering, default selection, renaming, and deletion.
- **APIs**: the Dify machine API is in `internal/app/apiv1.go` under `/api/v1/*`; browser and management JSON endpoints are in `internal/app/apiui.go`.
- **New packages**: add substantial functionality under `internal/<module>` and import it from `internal/app`.

## Architecture

~~~text
cmd/report-portal/       CLI subcommands and the thin HTTP-service entry point
internal/
  app/                   application core
    server.go            server startup, routes, sessions, and first-run setup
    apiv1.go             Dify machine API with Bearer-token authentication
    apiui.go             SPA and management JSON API with cookie authentication
    spa.go               deep-link fallback to index.html
    store.go             SQLite/PostgreSQL store for reports, accounts, tokens, and settings
    group.go             run grouping, category inference, and tabs
    roles.go             role and permission registry
    names.go             stock symbol-to-name mapping
    vendorfetch.go       controlled outbound access to quote and name vendors
    quote.go quote_cache.go quote_api.go
                          live quotes and daily charts with dual-source parsing and LRU caching
    pdf.go md.go          PDF and Markdown export
    user.go               account types
    templates/pdf.html    the only remaining server-side template
  config/                 generated infrastructure configuration
  version/                version, commit, and build-time metadata
  web/                    embedded frontend build output

web/                     React + Ant Design + Vite + TypeScript
  src/App.tsx             theme, locale, routing, and authentication
  src/api/                fetch wrappers and backend contract types
  src/auth.tsx prefs.tsx i18n.ts
                          session, preferences, and localized strings
  src/components/         AppLayout, Omnibox, ReportCard, Markdown, QuoteStrip, PriceChart
  src/pages/              Login, Home, Stock, Run, and management pages
  (build -> internal/web/dist -> embedded in the Go binary)
~~~

## License

[AGPL-3.0](LICENSE)
