# 研报门户 (report-portal)

[English](README.md) | [简体中文](README.zh-CN.md)

自托管的研究报告阅读门户，替代旧的 Mail Research Report System。前端采用 React、Ant Design 和 Vite，后端以单个 Go 二进制提供 JSON API，并通过 `go:embed` 内嵌前端构建产物。系统支持 SQLite 和 PostgreSQL，并提供 Docker 部署配置。

## 功能

- **统一搜索**：按股票代码或名称搜索，自动补全结果包含代码、名称、报告数量和最近报告日期。高级搜索支持类型、日期范围、关键字、来源和排序条件。
- **个股报告时间线**：聚合同一标的的全部报告，并按日期、大类和报告类型分层浏览。
- **统一历史数据**：旧门户的历史报告已导入本地数据库，与新报告使用同一数据源；旧门户的实时读取通道和一次性导入器均已随旧系统退役而删除。
- **实时行情与日 K 线**：个股页显示实时价格、涨跌、OHLC、成交量、成交额和行情时间，并提供 1 个月、3 个月、6 个月和 1 年区间的 SVG 日 K 线图。腾讯为主数据源，新浪为备用数据源；数据按需获取，采用 TTL 缓存和请求合并，不持久化，也不进行后台轮询。价格以整数分存储，涨跌幅采用数据源返回值，K 线使用不复权价格。解析器会校验数据源涨跌幅与现价、昨收之间的一致性，以降低上游字段变化导致的错误。北交所当前仅提供行情快照，不展示缺失或明显过期的日线数据。详见 [ADR 0028](docs/adr/0028-live-quotes.md)。
- **正文渲染**：`react-markdown` + GFM（表格/任务列表），旧报告 HTML 回退直渲。
- **导出**：Markdown（原生）+ PDF（镜像内 wkhtmltopdf）。
- **网页管理**：管理员可管理入口按钮、报告类型、账号、角色、API 令牌和接口文档。入口按钮与报告类型支持基于 `@dnd-kit` 的拖拽排序，并在操作完成后持久化。
- **多令牌**：Dify 接口支持多枚 Bearer 令牌，可分别配置备注、作用域（`all`、`ingest` 或 `query`）和有效期，并通过“系统设置”统一管理。
- **账号与角色**：角色注册表支持扩展；首次启动自动创建 `admin` 账号，并将随机生成的密码输出到终端。账号可配置有效期，到期后现有会话立即失效。
- **报告版本**：同一篇报告可保存内部版、对外版或客户版等多个版本。每个版本由独立运行产出，并可在“管理 → 报告版本”中配置可读账号或分组，以及报告申请范围（仅本人、本分组或全部）。阅读页会在存在多个可见版本时提供版本切换。读取权限与运行权限相互独立，可支持只读账号。详见 [ADR 0024](docs/adr/0024-report-versions.md)。
- **单点登录（SSO）**：SAML 2.0 与 OIDC/OAuth2 可同时启用，并通过“管理 → 单点登录”配置。SP 地址根据公开访问地址生成；组映射规则按顺序匹配，同时确定角色和组织单位，其中组织单位构成权限边界（见 ADR 0022）。首次登录自动创建账号默认关闭。密钥加密存储，接口不会返回密钥明文。详见 [ADR 0023](docs/adr/0023-sso-saml-oidc.md)。
- **两步验证与 Passkey**：本地账号可在“账号与安全”页自助启用 TOTP（含一次性恢复码，只显示一次），并注册 WebAuthn Passkey（可注册多个、可命名、可吊销，含克隆检测）。Passkey 是**第二因素**而非免密登录：先输密码，再用 Passkey 代替验证码。修改密码、增删 Passkey、开关两步验证都要求**再次验证身份**（密码或当前验证码），并与登录共用锁定策略；改密码会让其他所有设备下线。
- **主题与 i18n**：浅色/深色/跟随系统（antd `ConfigProvider` + `darkAlgorithm`）；中文/英文界面切换（`react-i18next` + antd locale，报告正文本身是中文数据不翻译）；响应式适配手机/平板/桌面。
- **自动初始化**：首次运行时，如果 `config.yaml` 不存在，系统会自动生成配置文件和随机 `secret_key`。配置文件仅保存基础设施参数，其他产品设置通过网页管理并存储在数据库中。

## 部署（Docker，推荐）

```bash
mkdir -p /opt/StockAnalysisPrediction-Report-Portal
cd /opt/StockAnalysisPrediction-Report-Portal
curl -O https://raw.githubusercontent.com/KazuhaHub/StockAnalysisPrediction-Report-Portal/main/docker-compose.yml
docker compose up -d
docker compose logs            # 查看首次启动生成的管理员初始密码
```

在浏览器中访问 `http://<host>:8790`。Compose 默认监听 `127.0.0.1:8790`；外部访问应通过反向代理提供 TLS。使用启动日志中的初始密码登录，并在“账号管理”中修改密码。

**更新**：`docker compose pull && docker compose up -d`（镜像标签：`:latest` 推荐正式版、`:beta` 最新已发布版（含预发布）、`:vYYYY.W.R` 固定版本）。

升级 CalVer 之前的部署前，请先阅读 [docs/releases/README.md](docs/releases/README.md)：首个 CalVer 版本只读取 **v0.4.72** 的数据库结构、不做任何转换，更旧的库必须先用 v0.4.72 启动一次，否则会被直接拒绝而不是半途升级。

首次启动会在 `./config/config.yaml` 生成默认配置。通常只需设置 `secret_key`，可使用 `openssl rand -hex 32` 生成随机值。

## 配置（config.yaml）

`config.yaml` 仅保存基础设施参数；账号、入口按钮、报告类型、令牌、Webhook 和应用等设置均通过网页管理并存储在数据库中。

```yaml
listen: ":8790"
secret_key: "长随机串"          # 会话签名，部署密钥
db_driver: "sqlite"            # sqlite(默认) | postgres
db_path: "data/portal.db"
# db_driver: "postgres"
# db_dsn: "postgres://user:pass@127.0.0.1:5432/reports?sslmode=disable"
```

### 备份与恢复

数据库保存账号、分组、报告正文、人工版及其修订历史、应用、令牌、Webhook 和系统设置。备份与恢复命令如下：

```bash
report-portal backup dump.jsonl          # 导出整库（"-" 表示写到标准输出）
report-portal backup - | gzip > dump.gz  # 通过标准输出流进行压缩

report-portal restore dump.jsonl         # 试运行：仅校验并输出统计，不修改数据库
report-portal restore dump.jsonl --force # 执行恢复：清空并替换数据库；操作前应停止服务
```

Docker 部署环境中的示例（`docker compose down -v` 会同时删除数据库卷）：

```bash
docker compose exec report-portal /app/report-portal backup - | gzip > dump-$(date +%F).gz
zcat dump-2026-09-04.gz | docker compose exec -T report-portal /app/report-portal restore - --force
```

运行特性：

- **跨驱动兼容**：同一备份格式可用于 SQLite 和 PostgreSQL，因此数据库迁移只需执行一次导出和一次恢复。
- **默认试运行**：不带 `--force` 时，命令会读取并校验完整备份，输出预计加载和删除的行数，但不修改数据库。该模式可用于验证备份完整性。
- **事务性替换**：恢复操作替换现有数据，而不是合并数据。整个过程在单个事务中完成；读取失败或操作中断时，数据库保持恢复前状态。
- **SQLite 导出期间的写入行为**：导出使用读事务以获得一致性快照，期间会短暂阻塞写入。基准数据为 5 万篇报告约 1.2 秒。PostgreSQL 不存在该写入阻塞。
- **备份容量**：未压缩备份的大小接近报告正文总量；5 万篇报告的基准数据约为 307 MiB。使用标准输出 `-` 可直接通过 `gzip` 压缩。
- **恢复耗时**：在 5 万篇报告的基准数据中，导出约需 1.2 秒，恢复约需 7.6 秒，试运行校验约需 1.7 秒。恢复过程不会持续输出进度；在事务提交前中断操作，后续打开数据库时会自动回滚。
- **敏感数据保护**：备份文件包含密码哈希和令牌哈希，文件权限设置为 `0600`。SSO 密钥在备份中保持加密，其解密依赖 `config.yaml` 中的 `secret_key`，因此应将配置目录与备份一并保存。

详见 [ADR 0027](docs/adr/0027-backup-and-restore.md)。

### 轮换 secret_key

`secret_key` 用于会话签名和保护 SSO 密钥环。OIDC/SAML 密钥由数据加密密钥加密，该数据密钥再由 `secret_key` 派生的密钥封装。直接替换 `secret_key` 会导致密钥环无法解密；系统会返回明确错误并保持原有数据不变。

轮换时应同时配置新旧密钥，完成一次重启后再删除旧密钥配置：

```yaml
secret_key: "新的长随机串"
secret_key_previous: "旧的长随机串" # 或环境变量 RP_SECRET_KEY_PREVIOUS
```

重启后，系统仅重新封装数据加密密钥，已存储的 SSO 密钥无需重新录入。日志提示轮换完成后，应删除 `secret_key_previous`，避免在磁盘上长期保留第二个有效密钥。

如果旧密钥永久丢失，需要删除 `meta` 表中的 `keyring_salt` 和 `keyring_wrapped_dek` 记录，并重新录入 SSO 密钥。轮换 `secret_key` 也会使所有现有登录会话失效。

### PostgreSQL

SQLite 适用于单实例和小规模部署，且无需独立数据库服务。多实例部署、较大数据规模或与 Dify 共用数据库服务时，可设置 `db_driver: postgres` 和 `db_dsn`。SQLite 与 PostgreSQL 使用同一数据访问层，PostgreSQL 路径由集成测试覆盖。

## Dify 入库接口

Dify 工作流通过 `POST /api/v1/reports` 入库，请求头为 `Authorization: Bearer <令牌>`。令牌可在“系统设置 → API 令牌”中创建，且作用域须包含 `ingest`。完整接口清单见“系统设置 → 接口说明”，机器可读的 OpenAPI 规范位于 `/api/openapi.json`。请求体示例：

```json
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
```

- 必填：`date`、`subtype`，以及 `symbol` 和 `title` 至少有一个（宏观/行业/策略等专题报告没有个股代码，靠 `title` 立身）；`body_md` 或兼容旧数据导入的 `body_html` 至少一项须含非空白正文。两者都有时以 Markdown 为准。
- **查询参数 `symbol` 不能是空字符串**：`GET /api/v1/reports?symbol=` 返回 400。空字符串表示上游未正确提供代码，不能作为“不限标的”处理，否则可能返回无关报告。入库允许不提供代码；专题报告使用 `title` 作为身份锚点。
- **`name`**：可选，入库当时的公司名快照。**借壳/改名后老报告仍显示当时名**（如老报告“鼎泰新材”不会被改成现名“顺丰控股”），显示时若与现名不同会两者都标出；不传则取名录里的现名。
- `kind`（大类）省略时按 `subtype` 推断，且不参与身份键，避免分类调整后产生重复报告。
- **身份键为 `symbol|date|subtype|title|version`**。相同身份键的再次入库会覆盖原报告，`run_id` 仅作为批次标签。`title` 始终参与身份键；`version` 省略时使用默认报告版本。
- **`source` 表示生产来源**。工作流建议使用 `dify/<模块>/<执行版本>` 格式；Portal 会识别末尾的 `V...` 并显示执行版本。该执行版本与身份键中的报告版本 `version` 是两个独立概念。

## 本地开发

前后端分离开发（热更新）：

```bash
# 1) 后端（JSON API，:8790）
cp config.example.yaml config.yaml           # 只填 secret_key；账号留空(首启自动生成)
go run ./cmd/report-portal                    # 终端打印 admin 密码

# 2) 前端（Vite dev，:5173，/api 代理到 :8790）
cd web && npm install && npm run dev
```

浏览器开 `http://localhost:5173`。前端类型检查 `npm run typecheck`。

一体化（后端直接服务构建好的前端，验证 `go:embed`）：

```bash
cd web && npm run build              # 产出 internal/web/dist/
go run ./cmd/report-portal           # 访问 :8790，SPA 由二进制内嵌服务
```

辅助命令：`go run ./cmd/report-portal hashpw '密码'`（生成 bcrypt 哈希）、`... adduser <名> <密码> admin`（恢复管理员访问）、`... fetchnames`（更新 A 股名称）、`... backup <文件|->` / `... restore <文件|-> [--force]`（导出或恢复完整数据库）、`... recompute-kinds`（分类调整后重新计算 `kind`）、`... freeze-names`（将当前名称固化到历史报告）以及 `... version`（显示版本、提交和构建时间）。

## 发布

版本号采用 CalVer：`vYYYY.W.R`，`YYYY` 为 ISO 周历年份，`W` 为该版本系列起始的 UTC ISO 周，`R` 为每次产物变更递增的修订号。从 release note 生成标签并推送：

```bash
scripts/tag-release.sh v2026.38.1
git push origin v2026.38.1
```

推送标签会校验标签、执行六平台交叉编译、推送固定的 `ghcr.io` 镜像标签，并创建一个**草稿** Release（含归档、`SHA256SUMS.txt` 与镜像 digest）。是否预发布由 GitHub Release 元数据决定，与标签名无关：把草稿发布为预发布或正式版、以及随后的 `:latest` / `:beta` 通道更新，都由 release-channels 工作流完成，它只把通道指向已发布的字节，不会重新构建。产物未变则沿用原版本号，产物有变必须新开版本号。详见 [ADR 0034](docs/adr/0034-calver-baseline-and-database-compatibility-reset.md)。

> 首次推送镜像后，如需支持未登录的 `docker compose pull`，请在仓库 Packages 设置中将对应 GHCR 包设为公开。

## 扩展点

- **角色**：`roles.go` 的 `roleRegistry` 加一项（角色→权限点），账号管理下拉与鉴权自动生效。
- **多语言**：在 `web/src/locales/*.json` 中维护各语言词条；组件通过 `useTranslation()` 和 `t('key')` 读取本地化文本。
- **报告类型**：系统从数据中发现报告类型，并在“类型管理”中提供分组、排序、默认项、重命名和增删功能；未匹配项使用默认分类。
- **接口**：Dify 机器接口（Bearer）全部在 `internal/app/apiv1.go`（`/api/v1/*`，唯一的机器接口面）；浏览器/管理 JSON 接口在 `internal/app/apiui.go`。
- **新包**：加功能就新建 `internal/<模块>`（如 `internal/auth` 做 SSO、`internal/dify` 直连 Dify），由 `internal/app` 引入。

## 结构

```
cmd/report-portal/       入口：CLI 子命令 + 启动 HTTP 服务（薄）
internal/
  app/                   应用核心（package app）
    server.go            RunServer/路由注册/会话/首启引导
    apiv1.go             Dify 机器接口 /api/v1/*(Bearer 令牌鉴权)——唯一的机器接口面
    apiui.go              浏览器 SPA + 管理后台的 JSON 接口(cookie 会话鉴权)
    spa.go               SPA 兜底(深链回 index.html)
    store.go              SQLite/Postgres 双驱动：报告 + 按钮 + 类型 + 账号 + 令牌 + 设置
    group.go              按 run 分组 + 类别推断 + tab 标签
    roles.go              角色/权限注册表(RBAC-lite)
    names.go              股票代码→名映射(内嵌种子 + 运行时抓全量)
    vendorfetch.go        行情/名称厂商的唯一出网通道(限长 + 查状态码 + 限频日志)
    quote.go quote_cache.go quote_api.go  实时行情 + 日K线：双源解析 + 防漂移闸门 + 内存 LRU + /api/quote
    pdf.go md.go          wkhtmltopdf 生成 PDF / markdown 渲染
    user.go               账号类型
    templates/pdf.html    唯一保留的服务端模板(PDF 导出)
  config/                 YAML 基础设施配置(缺省自动生成)
  version/                版本/commit/构建时间(-ldflags 注入)
  web/  (+ dist/)         go:embed 前端构建产物 + FS()

web/  (React + Ant Design + Vite + TS)
  src/App.tsx            ConfigProvider(主题/locale) + 路由 + 鉴权
  src/api/               fetch 封装 + 后端 JSON 契约类型
  src/auth.tsx prefs.tsx i18n.ts   会话 / 主题+语言偏好 / 界面词条
  src/components/        AppLayout · Omnibox · ReportCard · Markdown · QuoteStrip · PriceChart · icons
  src/pages/             Login · Home · Stock · Run · manage/(Links/Types/Users/Settings)
  (build → internal/web/dist → go:embed 进二进制)
```

## License

[AGPL-3.0](LICENSE)
