# 研报门户 (report-portal)

自托管的研究报告阅读门户，替代旧的 Mail Research Report System。单 Go 二进制、内嵌前端资源、SQLite/Postgres 双驱动、Docker 一键部署。

## 功能

- **一次 run 收一张卡**：同一次生成的多份报告（如重组 交易/舆情/基本面/汇总）聚成一张卡，点进去 **tab 切换**，默认打开"汇总"。
- **新旧共存**：新报告进本地库；旧门户历史报告通过读透其 API 共存显示，带【新】/【旧】徽标（旧门户最终淘汰，系统自给自足）。
- **卡片信息**：股票代码 + 公司名（内嵌 A 股代码→名映射，可自动补全）+ run 类别（并购重组/投资决策/深度研究/技术分析…）。
- **检索/筛选/翻页**：标的、类型、日期范围、关键字（标题/全文）、排序、来源，本地索引毫秒级；页码 + 省略号 + 跳转输入。
- **导出**：Markdown（原生）+ PDF（镜像内 wkhtmltopdf）。
- **网页管理**（管理员）：入口按钮、报告类型（按大类分组/**拖拽排序**/默认页/改名/增删）、账号（角色）、系统设置（旧门户凭据/同步间隔）。入口按钮与类型顺序都**拖拽调整、松手即存**（SortableJS，无构建）。
- **账号与角色**：可扩展的角色注册表（admin/user，易加更多）；首次启动自动创建 admin 并把密码打印到终端。
- **主题与响应式**：浅色/深色/跟随系统；**界面自适应手机/平板/桌面**（服务端渲染 + 响应式 CSS，无前端构建）；阅读页向下滚动**自动隐藏顶栏**、向上滚回来。**i18n**：多语言接口已预留（中文，英文可批量补）。
- **图标**：整套 **Tabler(MIT) webfont**（5800+ 图标，内嵌一份字体）；模板里 `{{icon "name"}}` 或直接 `<i class="ti ti-name">`，任意图标名即取即用。
- **零配置起步**：首次运行若无 `config.yaml` 会**自动生成**（含随机 `secret_key`）；config 只放基础设施，其余全在网页里管、存数据库。

## 部署（Docker，推荐）

```bash
mkdir -p /opt/StockAnalysisPrediction-Dify-Report-Portal
cd /opt/StockAnalysisPrediction-Dify-Report-Portal
curl -O https://raw.githubusercontent.com/KKazuhaK/StockAnalysisPrediction-Dify-Report-Portal/main/docker-compose.yml
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

## 本地开发

```bash
cp config.example.yaml config.yaml     # 只填 secret_key；账号留空(首启自动生成)
go run .                               # 默认 :8790，终端打印 admin 密码
```

辅助命令：`go run . hashpw '密码'`（生成 bcrypt）、`go run . adduser <名> <密码> admin`（兜底建管理员）、`go run . fetchnames`（抓全量 A 股名称）。

启动后后台自动同步旧门户元数据到本地库（`sync_interval_minutes`/系统设置里控制；未配置旧门户则跳过）。

## 发布

打 `v*` tag（`git tag v1.0.0 && git push origin v1.0.0`）触发 CI：跨平台编译 → 发 GitHub Release（二进制归档 + SHA256）→ 推多架构镜像到 `ghcr.io`。带 `-` 的（如 `v1.0.0-beta`）标记为预发布，只打 `:beta` 不动 `:latest`。

> 首次推镜像后，到仓库 Packages 设置把 ghcr 包设为 public，否则 `docker compose pull` 需登录。

## 扩展点（这是个正经项目 🙂）

- **图标**：整套 Tabler webfont 已内嵌，`{{icon "任意图标名"}}` 或 `<i class="ti ti-任意图标名">` 直接用（名字见 tabler.io/icons，去掉 `ti-` 前缀）。
- **角色**：`roles.go` 的 `roleRegistry` 加一项（角色→权限点），UI 下拉与鉴权自动生效。
- **多语言**：`i18n.go` 的 `messages["en"]` 批量补词条即生效；模板用 `{{t .Lang "key"}}`。
- **报告类型**：数据里自动发现，网页「类型管理」按大类分组/排序/指定默认页/改名/增删；未匹配自动兜底。

## 结构

```
main.go        路由/会话/handler/嵌入/后台同步/首启引导
store.go       SQLite/Postgres 双驱动：报告 + 旧元数据索引 + 按钮 + 类型 + 账号 + 设置
oldclient.go   读透旧门户(鉴权/同步/取正文，带缓存，可热更新凭据)
group.go       按 run 分组 + 类别推断 + tab 标签
roles.go       角色/权限注册表(RBAC-lite)
i18n.go        多语言词条 + T()
names.go       股票代码→名映射(内嵌种子 + 运行时抓全量)
pdf.go         wkhtmltopdf 生成 PDF
config.go      YAML 基础设施配置(缺省自动生成)
icons.go       {{icon}} → Tabler webfont 的 <i class="ti ti-x">
templates/ static/   页面/样式/Tabler webfont/flatpickr/SortableJS(go:embed 进二进制)
```

## License

[AGPL-3.0](LICENSE)
