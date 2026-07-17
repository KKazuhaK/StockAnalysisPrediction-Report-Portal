# 研报门户 (report-portal)

自托管的研究报告阅读门户，替代旧的 Mail Research Report System。**前端 React + Ant Design (Vite 构建)**，后端单 Go 二进制（JSON API + 用 `go:embed` 内嵌前端 `dist/`），SQLite/Postgres 双驱动、Docker 一键部署。

## 功能

- **omnibox 主搜索**：一个搜索框输代码或名字 → `AutoComplete` 补全（代码 + 名字 + 报告数 + 最近日期）→ 回车/选中进个股详情。**高级搜索**（类型/日期范围/关键字/来源/排序）折叠在可展开面板里，不占主路径。
- **个股详情时间线**：一只票的所有报告聚合，`Timeline` 选日期 → 大类 `Segmented` → 小文档 `Tabs` → 正文。
- **新旧共存**：新报告进本地库；旧门户历史报告通过读透其 API 共存显示，带【新】/【旧】徽标（旧门户最终淘汰，系统自给自足）。
- **正文渲染**：`react-markdown` + GFM（表格/任务列表），旧报告 HTML 回退直渲。
- **导出**：Markdown（原生）+ PDF（镜像内 wkhtmltopdf）。
- **网页管理**（管理员）：入口按钮、报告类型（按大类分组/**拖拽排序**/默认页/改名/增删）、账号（角色）、系统设置（旧门户凭据/同步间隔 + 多令牌 + 接口文档）。入口按钮与类型顺序都用 **@dnd-kit 拖拽排序**、松手即存。
- **多令牌**：Dify 接口鉴权支持多枚 Bearer 令牌（备注/作用域 all|ingest|query/有效期），在「系统设置」里管。
- **账号与角色**：可扩展的角色注册表（admin/user，易加更多）；首次启动自动创建 admin 并把密码打印到终端。
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

浏览器开 `http://<host>:8790`（compose 默认绑 `127.0.0.1:8790`，对外用 nginx 反代 + TLS），用打印的密码登录，进「账号管理」改密码；旧门户读透在「系统设置」里配。

**更新**：`docker compose pull && docker compose up -d`（镜像 `:latest` 稳定 / `:beta` 最新 / `:vX.Y.Z` 锁版本）。

首启会在 `./config/config.yaml` 生成默认配置，一般只需设 `secret_key`（`openssl rand -hex 32`）。

## 配置（config.yaml）

只放**基础设施**；其余（旧门户凭据、同步间隔、账号、入口按钮、类型…）都在网页里管、存数据库。

```yaml
listen: ":8790"
secret_key: "长随机串"          # 会话签名，部署密钥
db_driver: "sqlite"            # sqlite(默认) | postgres
db_path: "data/portal.db"
# db_driver: "postgres"
# db_dsn: "postgres://user:pass@127.0.0.1:5432/reports?sslmode=disable"
```

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
- **`symbol` 传了就不能是空字符串**：空 `symbol` 一律 400。空不等于「不限」——它意味着上游把代码弄丢了，静默放行会让报告落到别人名下。
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

辅助命令：`go run ./cmd/report-portal hashpw '密码'`（生成 bcrypt）、`... adduser <名> <密码> admin`（兜底建管理员）、`... fetchnames`（抓全量 A 股名称）、`... version`（版本/commit/构建时间）。

启动后后台自动同步旧门户元数据到本地库（系统设置里控制同步间隔；未配置旧门户则跳过）。

## 发布

打 `v*` tag（`git tag v1.0.0 && git push origin v1.0.0`）触发 CI：跨平台编译 → 发 GitHub Release（二进制归档 + SHA256）→ 推多架构镜像到 `ghcr.io`。带 `-` 的（如 `v1.0.0-beta`）标记为预发布，只打 `:beta` 不动 `:latest`。

> 首次推镜像后，到仓库 Packages 设置把 ghcr 包设为 public，否则 `docker compose pull` 需登录。

## 扩展点（这是个正经项目 🙂）

- **角色**：`roles.go` 的 `roleRegistry` 加一项（角色→权限点），账号管理下拉与鉴权自动生效。
- **多语言**：`web/src/i18n.ts` 的 `en` 资源批量补词条即生效；组件用 `useTranslation()` 的 `t('key')`。
- **报告类型**：数据里自动发现，网页「类型管理」按大类分组/排序/指定默认页/改名/增删；未匹配自动兜底。
- **接口**：Dify 机器接口（Bearer）在 `internal/app/server.go`；浏览器/管理 JSON 接口在 `internal/app/apiui.go`。
- **新包**：加功能就新建 `internal/<模块>`（如 `internal/auth` 做 SSO、`internal/dify` 直连 Dify），由 `internal/app` 引入。

## 结构

```
cmd/report-portal/       入口：CLI 子命令 + 启动 HTTP 服务（薄）
internal/
  app/                   应用核心（package app）
    server.go            RunServer/路由/会话/Dify Bearer 接口/首启引导
    apiui.go             浏览器 SPA + 管理后台的 JSON 接口(cookie 会话鉴权)
    spa.go               SPA 兜底(深链回 index.html)
    store.go             SQLite/Postgres 双驱动：报告 + 旧元数据 + 按钮 + 类型 + 账号 + 令牌 + 设置
    oldclient.go         读透旧门户(鉴权/同步/取正文，带缓存，可热更新凭据)
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
