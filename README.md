# 研报门户 (report-portal)

自托管的研究报告阅读门户，替代旧的 Mail Research Report System。**前端 React + Ant Design (Vite 构建)**，后端单 Go 二进制（JSON API + 用 `go:embed` 内嵌前端 `dist/`），SQLite/Postgres 双驱动、Docker 一键部署。

## 功能

- **omnibox 主搜索**：一个搜索框输代码或名字 → `AutoComplete` 补全（代码 + 名字 + 报告数 + 最近日期）→ 回车/选中进个股详情。**高级搜索**（类型/日期范围/关键字/来源/排序）折叠在可展开面板里，不占主路径。
- **个股详情时间线**：一只票的所有报告聚合，`Timeline` 选日期 → 大类 `Segmented` → 小文档 `Tabs` → 正文。
- **自给自足**：旧门户的历史报告已一次性导入本地库，与新报告同库同源；读透旧门户的实时通道和一次性导入器都已随旧门户退役而删除。
- **正文渲染**：`react-markdown` + GFM（表格/任务列表），旧报告 HTML 回退直渲。
- **导出**：Markdown（原生）+ PDF（镜像内 wkhtmltopdf）。
- **网页管理**（管理员）：入口按钮、报告类型（按大类分组/**拖拽排序**/默认页/改名/增删）、账号（角色）、系统设置（多令牌 + 接口文档）。入口按钮与类型顺序都用 **@dnd-kit 拖拽排序**、松手即存。
- **多令牌**：Dify 接口鉴权支持多枚 Bearer 令牌（备注/作用域 all|ingest|query/有效期），在「系统设置」里管。
- **账号与角色**：可扩展的角色注册表（admin/user，易加更多）；首次启动自动创建 admin 并把密码打印到终端。账号可设**有效期**，到期后当前会话立即失效。
- **报告版本**：同一篇报告可以有多个版本——完整的内部版、只有结论的对外版、以后还可以有客户版等。每个版本由各自的运行产出，在「管理 → 报告版本」里注册，并按版本配置**谁能读**（分组或单个账号）和**能看到谁申请的报告**（仅本人 / 本分组 / 全部）。阅读页在有多个可见版本时提供切换。可读权限与可运行权限**完全分离**，所以可以有只读不跑的客户。详见 [ADR 0024](docs/adr/0024-report-versions.md)。
- **单点登录（SSO）**：SAML 2.0 与 OIDC/OAuth2，可同时启用，在「管理 → 单点登录」里配置。SP 地址由公开访问地址推导并可一键复制；组映射规则按顺序命中，同时决定**角色与组织单位**（后者才是真正的权限边界，见 ADR 0022）；首次登录自动建号默认**关闭**。密钥加密存储、任何接口都不会回显。详见 [ADR 0023](docs/adr/0023-sso-saml-oidc.md)。
- **两步验证与 Passkey**：本地账号可在「账号与安全」页自助启用 TOTP（含一次性恢复码，只显示一次），并注册 WebAuthn Passkey（可注册多个、可命名、可吊销，含克隆检测）。Passkey 是**第二因素**而非免密登录：先输密码，再用 Passkey 代替验证码。修改密码、增删 Passkey、开关两步验证都要求**再次验证身份**（密码或当前验证码），并与登录共用锁定策略；改密码会让其他所有设备下线。
- **主题与 i18n**：浅色/深色/跟随系统（antd `ConfigProvider` + `darkAlgorithm`）；中文/英文界面切换（`react-i18next` + antd locale，报告正文本身是中文数据不翻译）；响应式适配手机/平板/桌面。
- **零配置起步**：首次运行若无 `config.yaml` 会**自动生成**（含随机 `secret_key`）；config 只放基础设施，其余全在网页里管、存数据库。

## 部署（Docker，推荐）

```bash
mkdir -p /opt/StockAnalysisPrediction-Report-Portal
cd /opt/StockAnalysisPrediction-Report-Portal
curl -O https://raw.githubusercontent.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/main/docker-compose.yml
docker compose up -d
docker compose logs            # 首启会打印随机管理员密码(admin / xxxxx)
```

浏览器开 `http://<host>:8790`（compose 默认绑 `127.0.0.1:8790`，对外用 nginx 反代 + TLS），用打印的密码登录，进「账号管理」改密码。

**更新**：`docker compose pull && docker compose up -d`（镜像 `:latest` 稳定 / `:beta` 最新 / `:vX.Y.Z` 锁版本）。

首启会在 `./config/config.yaml` 生成默认配置，一般只需设 `secret_key`（`openssl rand -hex 32`）。

## 配置（config.yaml）

只放**基础设施**；其余（账号、入口按钮、报告类型、令牌、Webhook、应用…）都在网页里管、存数据库。

```yaml
listen: ":8790"
secret_key: "长随机串"          # 会话签名，部署密钥
db_driver: "sqlite"            # sqlite(默认) | postgres
db_path: "data/portal.db"
# db_driver: "postgres"
# db_dsn: "postgres://user:pass@127.0.0.1:5432/reports?sslmode=disable"
```

### 备份与恢复

门户的一切都在库里——账号、分组、报告正文、人工版与修订历史、应用、令牌、Webhook、设置。所以有两条命令：

```bash
report-portal backup dump.jsonl          # 导出整库（"-" 表示写到标准输出）
report-portal backup - | gzip > dump.gz  # 想压缩就自己接一段管道

report-portal restore dump.jsonl         # 默认是试运行：只校验+报数，什么都不写
report-portal restore dump.jsonl --force # 真的恢复（会清空并替换整库，先停掉门户）
```

Docker 里这么用（注意 compose 的 `down -v` 会连库一起删）：

```bash
docker compose exec report-portal /app/report-portal backup - | gzip > dump-$(date +%F).gz
zcat dump-2026-09-04.gz | docker compose exec -T report-portal /app/report-portal restore - --force
```

几点值得知道：

- **一个格式，两种驱动**。同一个文件在 SQLite 和 Postgres 之间可以互导，所以「用大了，搬去 PG」和
  「搬回单文件」都只是导一次、恢复一次。
- **不带 `--force` 就是试运行**：会完整读一遍、校验一遍、报出会加载多少行、会删掉多少行，但一个字都不写。
  想确认「这份备份还能不能用」，在跑着的机器上直接跑这条就行。
- **恢复是替换，不是合并**，而且整个过程在一个事务里——文件中途断了，库还是原样。
- **备份文件本身是机密**（含密码哈希、令牌哈希），落盘权限是 `0600`。SSO 密钥在里面是密文，
  解它的 `secret_key` 在 `config.yaml` 里、**不在备份里**——所以 `config/` 目录要跟备份一起存，
  不然恢复出来的 SSO 是打不开的。

详见 [ADR 0027](docs/adr/0027-backup-and-restore.md)。

### 轮换 secret_key

`secret_key` 除了签会话，还包着 SSO 密钥环——存下来的 OIDC/SAML 密钥用一把数据密钥加密，而那把数据密钥被
`secret_key` 派生的密钥包起来。所以直接改 `secret_key` 会打不开密钥环；程序会**明确报错并且什么都不改**，
而不是重新生成一把（那会把已经存下的密钥全部变成永久无法解密）。

正确做法是把旧的那把一起写上，重启一次，然后删掉：

```yaml
secret_key: "新的长随机串"
secret_key_previous: "旧的那把"   # 或环境变量 RP_SECRET_KEY_PREVIOUS
```

重启后只重新包了**一行**——数据密钥本身没变，任何已存密钥都不用重新录入。日志会提示可以删掉
`secret_key_previous`；留着不危险，但那是磁盘上的第二把活密钥，所以每次启动都会提醒一次。
旧密钥彻底丢了的话，只能删掉 `meta` 表里的 `keyring_salt` / `keyring_wrapped_dek` 两行再重新录入 SSO 密钥。
换 `secret_key` 同时也会让所有人的登录会话失效，需要重新登录。

### Postgres

内部小用 SQLite 即可（单文件零依赖）。要多实例共享/上规模/跟 Dify 的 PG 合并，改 `db_driver: postgres` + `db_dsn` 即可，代码不变（已用真 PG 18 验证）。

## Dify 入库接口

Dify 工作流用 HTTP 节点 `POST /api/v1/reports` 入库，请求头 `Authorization: Bearer <令牌>`（令牌在「系统设置 → API 令牌」建，scope 含 `ingest`）。完整接口清单见网页「系统设置 → 接口说明」，机器可读的规格是 `/api/openapi.json`。请求体：

```json
{
  "symbol": "002594",
  "name": "比亚迪",
  "date": "2024-01-01",
  "kind": "投资决策",
  "subtype": "汇总",
  "title": "比亚迪 投资决策汇总",
  "body_md": "# 结论\n**买入**。",
  "run_id": "batch-2024-01",
  "source": "Dify",
  "tracking": [
    { "itype": "assumption", "content": "毛利率维持 20%", "status": "pending", "review_point": "下季度财报" }
  ]
}
```

- 必填：`date`、`subtype`，以及 `symbol` 和 `title` 至少有一个（宏观/行业/策略等专题报告没有个股代码，靠 `title` 立身）。
- **查询时 `symbol` 传了就不能是空字符串**：`GET /api/v1/reports?symbol=` 一律 400。空不等于「不限」——它意味着上游把代码弄丢了，静默放行会把别家公司的报告递给你。入库不受此限：专题报告本来就没代码，靠 `title` 立身。
- **`name`**：可选，入库当时的公司名快照。**借壳/改名后老报告仍显示当时名**（如老报告「鼎泰新材」不会被改成现名「顺丰控股」），显示时若与现名不同会两者都标出；不传则取名录里的现名。
- `kind`（大类）不传则按 `subtype` 推断，**它不参与身份**（否则大类改归类会把一份报告劈成两份）。
- **身份键 = `symbol|date|subtype|title`，同键覆盖更新**（重跑同一天同标题的同类型报告即覆盖，`run_id` 只是批次标签）。标题参与身份，所以同一天同类型的不同选题各自成篇、互不覆盖。

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

辅助命令：`go run ./cmd/report-portal hashpw '密码'`（生成 bcrypt）、`... adduser <名> <密码> admin`（兜底建管理员）、`... fetchnames`（抓全量 A 股名称）、`... backup <文件|->` / `... restore <文件|-> [--force]`（整库导出/恢复，见上文「备份与恢复」）、`... recompute-kinds`（改过分类后重算 kind）、`... freeze-names`（把当前名称固化到历史报告上）、`... version`（版本/commit/构建时间）。

## 发布

打 `v*` tag（`git tag v1.0.0 && git push origin v1.0.0`）触发 CI：跨平台编译 → 发 GitHub Release（二进制归档 + SHA256）→ 推多架构镜像到 `ghcr.io`。带 `-` 的（如 `v1.0.0-beta`）标记为预发布，只打 `:beta` 不动 `:latest`。

> 首次推镜像后，到仓库 Packages 设置把 ghcr 包设为 public，否则 `docker compose pull` 需登录。

## 扩展点（这是个正经项目 🙂）

- **角色**：`roles.go` 的 `roleRegistry` 加一项（角色→权限点），账号管理下拉与鉴权自动生效。
- **多语言**：`web/src/i18n.ts` 的 `en` 资源批量补词条即生效；组件用 `useTranslation()` 的 `t('key')`。
- **报告类型**：数据里自动发现，网页「类型管理」按大类分组/排序/指定默认页/改名/增删；未匹配自动兜底。
- **接口**：Dify 机器接口（Bearer）全部在 `internal/app/apiv1.go`（`/api/v1/*`，唯一的机器接口面）；浏览器/管理 JSON 接口在 `internal/app/apiui.go`。
- **新包**：加功能就新建 `internal/<模块>`（如 `internal/auth` 做 SSO、`internal/dify` 直连 Dify），由 `internal/app` 引入。

## 结构

```
cmd/report-portal/       入口：CLI 子命令 + 启动 HTTP 服务（薄）
internal/
  app/                   应用核心（package app）
    server.go            RunServer/路由注册/会话/首启引导
    apiv1.go             Dify 机器接口 /api/v1/*(Bearer 令牌鉴权)——唯一的机器接口面
    apiui.go             浏览器 SPA + 管理后台的 JSON 接口(cookie 会话鉴权)
    spa.go               SPA 兜底(深链回 index.html)
    store.go             SQLite/Postgres 双驱动：报告 + 按钮 + 类型 + 账号 + 令牌 + 设置
    group.go             按 run 分组 + 类别推断 + tab 标签
    roles.go             角色/权限注册表(RBAC-lite)
    names.go             股票代码→名映射(内嵌种子 + 运行时抓全量)
    pdf.go md.go         wkhtmltopdf 生成 PDF / markdown 渲染
    user.go              账号类型
    templates/pdf.html   唯一保留的服务端模板(PDF 导出)
  config/                YAML 基础设施配置(缺省自动生成)
  version/               版本/commit/构建时间(-ldflags 注入)
  web/  (+ dist/)        go:embed 前端构建产物 + FS()

web/  (React + Ant Design + Vite + TS)
  src/App.tsx            ConfigProvider(主题/locale) + 路由 + 鉴权
  src/api/               fetch 封装 + 后端 JSON 契约类型
  src/auth.tsx prefs.tsx i18n.ts   会话 / 主题+语言偏好 / 界面词条
  src/components/        AppLayout · Omnibox · ReportCard · Markdown · icons
  src/pages/             Login · Home · Stock · Run · manage/(Links/Types/Users/Settings)
  (build → internal/web/dist → go:embed 进二进制)
```

## License

[AGPL-3.0](LICENSE)
