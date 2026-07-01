# 研报门户 (report-portal)

自托管的研究报告阅读门户,替代旧的 Mail Research Report System。

- **一次 run 收一张卡**:同一次 Dify 生成的多份报告(重组交易/舆情/基本面/汇总…)聚成一张卡,点进去 **tab 切换**,默认打开「汇总」。
- **新旧共存**:新报告进本地库;旧门户 6000+ 篇通过读透其 API 共存显示,带【新】/【旧】徽标。旧门户最终淘汰,新系统完全自给自足。
- **筛选/检索**:标的、类型、日期范围、关键字(标题/全文)、排序、分页 —— 全在本地索引上跑,毫秒级。
- **导出 MD / PDF**(PDF 由镜像内 wkhtmltopdf 生成,不依赖旧门户)。
- **生成入口按钮**可视化增删改。账号密码登录、深色主题。

单 Go 二进制 + 内嵌模板/静态资源;数据用 SQLite(以后可换 Postgres)。

## 部署(Docker,推荐)

```bash
mkdir -p /opt/report-portal && cd /opt/report-portal
curl -O https://raw.githubusercontent.com/KKazuhaK/StockAnalysisPrediction-Dify-Report-Portal/main/docker-compose.yml
docker compose up -d
# 首启会在 ./config/config.yaml 生成默认配置，编辑它：
#   - secret_key: openssl rand -hex 32
#   - old_portal: 旧门户地址/账号/密码（共存期）
#   - users: 见下方生成密码哈希
docker compose restart
```

**更新**:`docker compose pull && docker compose up -d`(镜像 `:latest` 稳定 / `:beta` 最新 / `:vX.Y.Z` 锁版本)。

对外用宿主机 nginx 反代到 `127.0.0.1:8790` + TLS。

### 生成登录密码哈希
```bash
docker run --rm ghcr.io/kkazuhak/stockanalysisprediction-dify-report-portal:latest hashpw '你的密码'
# 输出的 $2a$... 贴到 config.yaml 的 users[].password_hash
```

## 本地开发

```bash
cp config.example.yaml config.yaml   # 填好 secret_key/账号/旧门户
go run . hashpw 'demo123'            # 生成哈希填进 config.yaml
go run .                            # 启动，默认 :8790
```

启动后后台自动同步旧门户元数据到本地 SQLite(`sync_interval_minutes` 控制间隔)。

## 发布

打 `v*` tag(如 `git tag v1.0.0 && git push --tags`)触发 CI:
跨平台编译 → 发 GitHub Release(带二进制归档 + SHA256)→ 推多架构镜像到 `ghcr.io`。
`vX.Y.Z-beta` 之类带 `-` 的标记为预发布,只打 `:beta` 不动 `:latest`。

## 结构
```
main.go        路由/会话/handler/嵌入/后台同步
store.go       SQLite：新报告 + 旧元数据索引 + 按钮
oldclient.go   读透旧门户（鉴权/同步/取正文，带缓存）
group.go       按 run 分组 + tab 标签
pdf.go         wkhtmltopdf 生成 PDF
config.go      YAML 配置
templates/ static/   页面与样式（go:embed 进二进制）
```

## License

[AGPL-3.0](LICENSE)
