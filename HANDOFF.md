# HANDOFF — 研报门户 React(Ant Design)重构

> 给新窗口的交接。本窗口原是 Dify 工作流侧,Portal 的 React 重构在新窗口做。
> 后端 Go 已基本就绪(JSON API + DB),前端要从 Go SSR 换成 React + antd。

## 1. 项目 & 现状
- 自托管研报门户,替代旧 Mail Research Report System。
- 仓库 `github.com/KKazuhaK/StockAnalysisPrediction-Dify-Report-Portal`,本地 `~/Codes/StockAnalysisPrediction-Dify-Report-Portal`,**AGPL-3.0**,分支 `main`,HEAD `c063ed0`。
- 现状 = **Go SSR + 手写 CSS 的完整可用版**(要被 React 取代);后端 JSON API 大半已建。
- 换框架的动因:手写 UI 的小 bug(暗色漏配、空状态、组件不一致)——用组件库根治。

## 2. 前端决策:**React + Ant Design (antd)**(不用 MUI)
选 antd 的理由:它是**数据密集后台/仪表盘**专用,我们手搓过的东西它全有一等公民组件;**中文一流**、**暗色开箱即用**。组件映射:
| 我们的 UI | antd 组件 |
|---|---|
| 首页搜索框(代码/名字补全) | `AutoComplete` / `Select(showSearch)` |
| 报告卡片列表 | `Card` + `Row/Col` / `List` |
| 大类/小文档 徽标 | `Tag` |
| 个股详情**时间线** | **`Timeline`** |
| 大类 tab、小文档 tab | `Tabs` / `Segmented` |
| 阅读正文(markdown) | `react-markdown` + antd `Typography` |
| 后台表格(类型/令牌/账号) | `Table` |
| 拖拽排序(类型/入口) | `Table` + `dnd-kit`(或 antd `Table` 可排序行) |
| 日期范围(高级搜索) | `DatePicker.RangePicker`(中文 locale) |
| 系统设置分区 | `Tabs` |
| 表单 | `Form` |
- 暗色:antd v5 `ConfigProvider` + `theme.darkAlgorithm`,跟随系统/手动切换。
- i18n:antd `ConfigProvider locale` + `react-i18next`(中文为主,英文预留)。
- 构建:**Vite + React + TS**;产物 `dist/` 用 Go `go:embed` 打进二进制。

## 3. 搜索 UX(重点,新设计)
既然一只票的所有报告已聚合(个股详情有时间线 + tab),主搜索**不再需要一堆日期/类型筛选**:
- **主路径 = omnibox**:一个搜索框,输代码或名字 → `AutoComplete` 补全(代码 + 名字 + 报告数 + 最近日期,数据来自 `GET /api/symbols`)→ 选中跳 `/stock/{code}`(默认落最新那天的汇总)。
- **高级搜索 = 可展开**:一个"高级搜索"折叠面板(`Collapse` / 展开按钮),里面才放类型(大类/小文档)、日期范围、关键字/全文、来源等细筛,走 `GET /api/reports?...`。不占主路径。
- **没搜到时的兜底(重要)**:omnibox 只返回"有报告的票"。搜不到时**不要只显示"无结果"**,给一个「在报告内容里搜『XXX』」入口 → 调 `GET /api/reports?q=XXX`(全文搜正文,用户常搜的是主题词如"增持/估值"而非股名)。可进一步区分:若 `q` 命中 stocks 表但无报告 → 提示"该股票暂无报告";否则 → 引导全文搜。(如需"连没报告的票也返回",给 `/api/symbols` 加个开关即可。)

## 4. 后端(Go,保持;退成 JSON API)
- 栈:`net/http`(Go1.22 路由)+ `modernc.org/sqlite`(纯 Go,CGO off)+ `jackc/pgx`(PG 双驱动)+ `goldmark`(markdown)+ bcrypt + 签名 cookie。文件:`main.go store.go oldclient.go group.go names.go pdf.go md.go config.go icons.go`。
- `/api/*` 已建(见 §6),React 直接消费;`/manage/*` 网页管理端点要在 React 里重做为 API+页面(或复用现有 POST 端点)。
- 会话:签名 cookie(`rp_session`);API:`Authorization: Bearer <令牌>`。

## 5. 数据库 schema(SQLite / PG 双驱动,`store.go`)
- **reports**(新报告,一行=一篇小文档):`id, uid UNIQUE, run_id, symbol, rdate, kind(大类), rtype(小文档), title, source, sent_at, body_md, body_html`;索引 `(symbol,rdate)`,`rdate`,`run_id`。
  - **身份键 = `symbol|date|kind|rtype` → uid;同键再入库=覆盖更新(最新赢,去重)**。`run_id` 只当批次标签。
- **type_config**(类型注册表):`name(小文档,PK唯一), kind(大类,显式), ord, is_summary, label`。入库自动登记;后台改大类会传播到 reports。**注意:大类本身没有内容,纯分类;`汇总` 小文档是事实上的大类级总览。**
- **tracking_items**(结构化假设/跟踪):`report_uid, symbol, itype(assumption|tracking), content, status(pending|confirmed|refuted), review_point, created_at`。
- **api_tokens**(多令牌):`token UNIQUE, name, scope(all|ingest|query), expires_at, last_used_at`。
- **links / users / meta / old_meta**(入口按钮 / 账号 / kv设置 / 旧门户读透元数据)。
- **stocks**(股票代码→名字,B 方案,**已建好**):`code PK, name, updated_at`;索引 `name`。
  - 启动时 + `fetchnames` 抓 eastmoney 后自动 upsert 进表(`store.SyncStocks`)。demo 里已 5874 条。
  - **按名字搜已成立**:`/api/symbols?q=` 匹配代码 **或** 名字(`code LIKE ? OR name LIKE ?`),和"有报告的票"取交集。reports 仍只存 symbol,名字 join stocks。

## 6. API 清单(全部 Bearer 令牌;入库需 scope 含 ingest,查询需含 query)
- `POST /api/reports` 入库(可带 `tracking[]`),同键覆盖。字段:`{symbol,date,kind,subtype,title,body_md,run_id,tracking:[{itype,content,status,review_point}]}`。
- `GET /api/reports?symbol=&q=&kind=&subtype=&date=today|since=&until=&limit=&with_body=` 查/搜/存在性 → `{has,count,reports:[{uid,symbol,name,date,kind,subtype,title,run_id,source}]}`。
- `GET /api/reports/manifest?symbol=` 清单(dates/kinds/subtypes/counts)。
- `GET /api/runs?symbol=&date=` 报告组　`GET /api/symbols?q=&limit=` 股票清单/补全(**按代码或名字搜**,omnibox 数据源)。
- `GET /api/tracking?symbol=&status=` 结构化假设/跟踪　`GET /api/report?uid=` 单篇完整正文。

## 7. 前端页面清单(React + antd 重做)
- 登录。
- **首页**:omnibox 主搜 + 可展开高级搜索;卡片列表(新旧共存,新卡多大类 tag);分页。
- **个股详情** `/stock/{code}`:`Timeline`(默认最新在右)→ 大类 `Tabs` → 小文档 `Tabs` → 正文;MD/PDF 导出。
- **阅读**:markdown 渲染;长文顶栏可隐藏。
- **管理**:入口按钮(拖拽)/类型(大类下拉可改 + 拖拽 + 增删)/账号(角色)/系统设置(旧门户与同步 + 多令牌 + 接口文档,分 tab)。
- 主题浅/深/跟随系统;i18n 中文(英文预留);响应式。

## 8. 部署(不变:单二进制 + compose pull)
- Vite `dist/` → Go `go:embed` → 一个二进制同时服务 SPA(fallback index.html)+ API + 旧门户读透 + PDF(镜像内 wkhtmltopdf)。
- 打 `v*` tag → CI 跨平台编译 + 推 ghcr 多架构镜像;`docker compose pull && up -d` 更新。套路抄 `KazuhaHub/Passwall-Sub-Panel`。
- config.yaml **只放基础设施**(listen/secret_key/db),缺失自动生成;其余全存 DB。

## 9. 硬性规矩(务必遵守)
- **commit 不加 Co-Authored-By**。
- **不做 DB 迁移**:测试阶段直接删 `data/portal.db` 重建。
- **config 只放基础设施**,其余存 DB、网页管。
- **大厂/开源项目质量**,不要小项目心态。**日志用英文**(数据值中文无所谓)。

## 10. 待办
1. ~~stocks 表 + 按名字搜~~ **已完成**(§5/§6)。
2. **全量名录**:生产机(能连 eastmoney)跑 `report-portal fetchnames`;域名用 `push2.eastmoney.com`(`82.push2` 被墙),diff 兼容对象/数组(已修)。启动/抓取后自动同步进 stocks 表。
3. **全文搜规模化**:现用 LIKE,上万篇加 SQLite FTS5(中文 `trigram`)/ PG `pg_trgm`。
4. React 版就位后删老 SSR 模板/CSS。

## 本地跑
`cd local-build && RP_CONFIG=./config.yaml /tmp/rp-portal`(或 `go run ..`),`:8790`,admin/demo123(`local-build/` 已 gitignore)。令牌在「系统设置」。旧门户读透 base `http://47.237.204.217:8888`(账号 nick8374818287,密码只在运行时 DB,别硬编码/入库)。
