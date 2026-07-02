// Reference data for the Dify machine API, rendered by the 接口说明 tab.
// Kept as data (not JSX) so it is easy to test and to keep in sync with the handlers.

export interface ApiParam {
  name: string
  in: 'query' | 'body' | 'path' | 'header'
  type: string
  required: boolean
  desc: string
}
export interface ApiError {
  code: number
  when: string
}
export interface ApiEndpoint {
  method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  path: string
  scope: string
  summary: string
  params: ApiParam[]
  requestExample: string
  responseExample: string
  errors: ApiError[]
  notes: string
}

export const API_CONVENTIONS = `**Base URL** — the origin the Portal is served on (no hardcoded host, no version prefix). All paths are absolute from that origin.

**Authentication** — two mechanisms:
- **Bearer token** (machine): header \`Authorization: Bearer <token>\`. The token must exist, not be expired, and cover the operation's scope.
- **Cookie session** (browser): a logged-in session automatically satisfies **query** scope on read endpoints. It does **not** grant **ingest** — \`POST /api/reports\` requires a Bearer \`ingest\`/\`all\` token.

**Scopes** — each token is \`ingest\`, \`query\`, or \`all\` (\`all\` passes everything). Ingest needs \`ingest\`; every GET needs \`query\`. Manage tokens on this page.

**Content type** — successful responses are JSON. ⚠️ **Errors are the exception: they are plain text** (\`http.Error\`), not a JSON \`{error}\` object. Don't parse error bodies as JSON — branch on the HTTP status.

**Dates** — \`YYYY-MM-DD\`. Not validated; stored and range-compared as strings (so always zero-pad). The \`time\` field is stored verbatim.

**Idempotent upsert** — reports are keyed by \`uid\` (explicit) or the synthesized \`symbol|date|kind|rtype\`. Re-ingesting the same identity **overwrites** the row. \`run_id\` is only a batch label and is not part of identity. Ingest always returns 200 and does not say created-vs-updated.

**Query limits** — \`GET /api/reports\` supports \`offset\` pagination and returns \`total\`, and filters by \`source\`/\`run_id\`; ordering is always date-desc. \`subtype\` and \`rtype\` are aliases (\`subtype\` wins).

**Public** — \`GET /healthz\` and \`GET /api/version\` need no auth.`

export const API_ENDPOINTS: ApiEndpoint[] = [
  {
    method: 'POST',
    path: '/api/reports',
    scope: 'ingest',
    summary: '入库（幂等 upsert）一篇报告，可附带 tracking 假设项。',
    params: [
      { name: 'symbol', in: 'body', type: 'string', required: true, desc: '股票代码（如 300750）。缺失/空 → 400。未知代码会触发后台补名。' },
      { name: 'date', in: 'body', type: 'string', required: true, desc: '报告日期 YYYY-MM-DD。缺失/空 → 400。未做格式校验；time 为空时以它兜底。' },
      { name: 'uid', in: 'body', type: 'string', required: false, desc: '显式身份键。不传则由服务端合成 symbol|date|kind|rtype；相同 uid 再次入库会覆盖。' },
      { name: 'run_id', in: 'body', type: 'string', required: false, desc: '批次/生成轮次标签。存储并返回，但不参与身份键。' },
      { name: 'name', in: 'body', type: 'string', required: false, desc: '入库当时公司名快照。不传则取当时 stocks 现名。历史不可变：日后改名/借壳不会重贴。' },
      { name: 'kind', in: 'body', type: 'string', required: false, desc: '大类（投资决策/深度研究/重组决策/事件监测/其他）。为空则按类型注册表→runKind 推断。参与合成 uid。' },
      { name: 'subtype', in: 'body', type: 'string', required: false, desc: '小类（如 汇总/财务分析）。是 rtype 的别名，二者都传时 subtype 胜出。会自动注册到该 kind 下。' },
      { name: 'rtype', in: 'body', type: 'string', required: false, desc: 'subtype 的别名（subtype 未传时使用）。' },
      { name: 'title', in: 'body', type: 'string', required: false, desc: '标题。列表显示 + 导出文件名。' },
      { name: 'source', in: 'body', type: 'string', required: false, desc: '来源/作者。存储并返回，但查询接口不能按它过滤。' },
      { name: 'time', in: 'body', type: 'string', required: false, desc: '用于同日同轮次内排序的时间戳。原样存储；为空则取 date。' },
      { name: 'body_md', in: 'body', type: 'string', required: false, desc: 'Markdown 正文。body_html 为空时由它自动渲染 HTML。' },
      { name: 'body_html', in: 'body', type: 'string', required: false, desc: '预渲染 HTML 正文。为空则从 body_md 派生。' },
      { name: 'tracking', in: 'body', type: 'array<object>', required: false, desc: '假设/跟踪项：{itype(assumption|tracking), content, status(空=pending), review_point}。数组非空时会覆盖该 uid 的全部 tracking 行。' },
    ],
    requestExample: `curl -s -X POST https://portal.example.com/api/reports \\
  -H "Authorization: Bearer <ingest-token>" \\
  -H "Content-Type: application/json" \\
  -d '{
    "run_id": "run-20260702-abc",
    "symbol": "300750",
    "name": "宁德时代",
    "date": "2026-07-02",
    "kind": "投资决策",
    "subtype": "汇总",
    "title": "宁德时代 2026H1 投资决策汇总",
    "source": "dify-workflow/dept-1",
    "time": "2026-07-02 08:15:00",
    "body_md": "## 结论\\n维持增持...",
    "tracking": [
      {"itype":"assumption","content":"H2 出货量同比 +20%","status":"pending","review_point":"2026-10-31 三季报"}
    ]
  }'`,
    responseExample: `{"ok":true,"uid":"300750|2026-07-02|投资决策|汇总","created":true}`,
    errors: [
      { code: 401, when: 'Bearer 缺失/无效，或无 ingest（且非 all）作用域，或已过期。cookie 会话不满足 ingest。正文纯文本 `unauthorized`。' },
      { code: 400, when: 'JSON 非法或超过 16MB（`bad json: ...`），或 symbol/date 缺失（`symbol and date required`）。' },
      { code: 500, when: '数据库写入失败。正文 `db: <detail>`。' },
    ],
    notes: '幂等：身份 = uid 或合成 symbol|date|kind|rtype，再次入库整行覆盖。响应 created=true 表示新建、false 表示覆盖。成功恒 200（非 201）。正文上限 16MB。tracking 仅在数组非空时更新，且是整体替换（单项改状态见 PATCH /api/tracking/{id}）。撤回见 DELETE /api/report。暂无批量入库。日志 `ingest <symbol> <date> created=<bool>`。',
  },
  {
    method: 'GET',
    path: '/api/reports',
    scope: 'query',
    summary: '按标的和/或关键字搜索历史报告。',
    params: [
      { name: 'symbol', in: 'query', type: 'string', required: false, desc: '精确代码匹配。symbol、q、run_id 至少给一个。为空则全库搜。' },
      { name: 'q', in: 'query', type: 'string', required: false, desc: '关键字，匹配标题/代码/现名/正文（LIKE %q%）。' },
      { name: 'kind', in: 'query', type: 'string', required: false, desc: '精确大类过滤。' },
      { name: 'source', in: 'query', type: 'string', required: false, desc: '精确来源过滤（须与 symbol/q/run_id 同用）。' },
      { name: 'run_id', in: 'query', type: 'string', required: false, desc: '精确批次/生成轮次过滤，可单独使用（取该轮全部报告）。' },
      { name: 'subtype', in: 'query', type: 'string', required: false, desc: '精确小类过滤。rtype 的别名，二者都传时 subtype 胜。' },
      { name: 'rtype', in: 'query', type: 'string', required: false, desc: 'subtype 的别名。' },
      { name: 'since', in: 'query', type: 'string', required: false, desc: '起始日期（rdate >= since）。date 存在时被覆盖。' },
      { name: 'until', in: 'query', type: 'string', required: false, desc: '截止日期（rdate <= until）。date 存在时被覆盖。' },
      { name: 'date', in: 'query', type: 'string', required: false, desc: '精确某天：把 since/until 都设为它。特殊值 today = 服务器当天。覆盖 since/until。' },
      { name: 'limit', in: 'query', type: 'integer', required: false, desc: '每页条数。<=0 或 >200 时取默认 20。' },
      { name: 'offset', in: 'query', type: 'integer', required: false, desc: '翻页偏移，默认 0。配合 total 分页。' },
      { name: 'with_body', in: 'query', type: 'string', required: false, desc: '=1 或 true 时每条含 body_md，否则省略正文。' },
    ],
    requestExample: `curl -s -G https://portal.example.com/api/reports \\
  -H "Authorization: Bearer <query-token>" \\
  --data-urlencode "symbol=300750" \\
  --data-urlencode "kind=投资决策" \\
  --data-urlencode "date=today" \\
  --data-urlencode "with_body=1" \\
  --data-urlencode "limit=20"`,
    responseExample: `{
  "symbol": "300750",
  "q": "",
  "has": true,
  "count": 1,
  "total": 1,
  "offset": 0,
  "limit": 20,
  "reports": [
    {
      "uid": "300750|2026-07-02|投资决策|汇总",
      "run_id": "run-20260702-abc",
      "symbol": "300750",
      "name": "宁德时代",
      "date": "2026-07-02",
      "kind": "投资决策",
      "subtype": "汇总",
      "title": "宁德时代 2026H1 投资决策汇总",
      "source": "dify-workflow/dept-1",
      "body_md": "## 结论\\n维持增持..."
    }
  ]
}`,
    errors: [
      { code: 401, when: '无有效 cookie 会话且无 query/all 令牌。`unauthorized`。' },
      { code: 400, when: 'symbol、q、run_id 都未给。`symbol, q or run_id required`。' },
      { code: 500, when: '数据库查询失败。`db: <detail>`。' },
    ],
    notes: '恒按 rdate DESC, sent_at DESC 排序（无排序参数）。total = 命中总数，配合 limit/offset 翻页。has = total>0。name 为实时现名（非快照）。q 匹配 标题/代码/现名/正文。date=today 用服务器本地日期。',
  },
  {
    method: 'GET',
    path: '/api/reports/manifest',
    scope: 'query',
    summary: '预探某标的有哪些报告：总数、按日期分布、全部大类/小类。',
    params: [
      { name: 'symbol', in: 'query', type: 'string', required: true, desc: '股票代码。缺失/空 → 400。只统计新报告（不含旧 old_meta）。' },
    ],
    requestExample: `curl -s -G https://portal.example.com/api/reports/manifest \\
  -H "Authorization: Bearer <query-token>" \\
  --data-urlencode "symbol=300750"`,
    responseExample: `{
  "symbol": "300750",
  "name": "宁德时代",
  "total": 6,
  "dates": [
    {"date": "2026-07-02", "count": 4, "kinds": ["投资决策"]},
    {"date": "2026-05-10", "count": 2, "kinds": ["深度研究", "事件监测"]}
  ],
  "kinds": ["事件监测", "投资决策", "深度研究"],
  "subtypes": ["汇总", "财务分析", "综合深度研究", "舆情分析"]
}`,
    errors: [
      { code: 401, when: '无有效 cookie 会话且无 query/all 令牌。`unauthorized`。' },
      { code: 400, when: 'symbol 缺失/空。`symbol required`。' },
    ],
    notes: '作为 Dify 取数前的廉价预检。kinds/subtypes 是跨全部日期去重排序；dates[].kinds 是当天出现的大类。total 只计新报告。无 500 分支（查询出错返回空清单）。',
  },
  {
    method: 'GET',
    path: '/api/report',
    scope: 'query',
    summary: '按 uid（或内部 rid）取单篇完整正文。',
    params: [
      { name: 'uid', in: 'query', type: 'string', required: false, desc: '报告身份键（入库返回值）。优先使用；非空时忽略 rid。' },
      { name: 'rid', in: 'query', type: 'string', required: false, desc: '内部行 id：n<rowid>=新报告 / o<id>=旧报告。仅 uid 为空时用。旧 o<id> 会即时回源旧门户取正文。' },
    ],
    requestExample: `curl -s -G https://portal.example.com/api/report \\
  -H "Authorization: Bearer <query-token>" \\
  --data-urlencode "uid=300750|2026-07-02|投资决策|汇总"`,
    responseExample: `{
  "uid": "300750|2026-07-02|投资决策|汇总",
  "run_id": "run-20260702-abc",
  "symbol": "300750",
  "date": "2026-07-02",
  "kind": "投资决策",
  "subtype": "汇总",
  "title": "宁德时代 2026H1 投资决策汇总",
  "source": "dify-workflow/dept-1",
  "body_md": "## 结论\\n维持增持...",
  "body_html": "<h2>结论</h2>\\n<p>维持增持...</p>"
}`,
    errors: [
      { code: 401, when: '无有效 cookie 会话且无 query/all 令牌。`unauthorized`。' },
      { code: 404, when: 'uid/rid 都未命中（不存在或 rid 非法）。`not found`。' },
    ],
    notes: '总是同时返回 body_md 与 body_html（不同于列表接口）。此接口不含 name 字段。uid 优先于 rid；旧 o<id> 触发回源，回源失败会 404。',
  },
  {
    method: 'GET',
    path: '/api/runs',
    scope: 'query',
    summary: '报告组视图：每个 (标的, 日期, 大类) 生成轮次一行。',
    params: [
      { name: 'symbol', in: 'query', type: 'string', required: true, desc: '股票代码。缺失/空 → 400。只分组新报告。' },
      { name: 'date', in: 'query', type: 'string', required: false, desc: '可选精确某天（rdate = date）。不传则返回全部日期。' },
    ],
    requestExample: `curl -s -G https://portal.example.com/api/runs \\
  -H "Authorization: Bearer <query-token>" \\
  --data-urlencode "symbol=300750"`,
    responseExample: `{
  "symbol": "300750",
  "name": "宁德时代",
  "has": true,
  "count": 2,
  "runs": [
    {"symbol":"300750","date":"2026-07-02","kind":"投资决策","run_id":"run-20260702-abc","subtypes":["汇总","财务分析","研报分析","行业分析"],"count":4},
    {"symbol":"300750","date":"2026-05-10","kind":"深度研究","run_id":"run-20260510-xyz","subtypes":["综合深度研究"],"count":1}
  ]
}`,
    errors: [
      { code: 401, when: '无有效 cookie 会话且无 query/all 令牌。`unauthorized`。' },
      { code: 400, when: 'symbol 缺失/空。`symbol required`。' },
    ],
    notes: '一个 run = 同 标的+日期+大类。run_id 取组内 MAX。subtypes 为组内去重小类，count 为报告数。按 date DESC, kind 排序。无 500 分支。',
  },
  {
    method: 'GET',
    path: '/api/symbols',
    scope: 'query',
    summary: '列出有报告的股票（自动补全/omnibox），按报告数排序。',
    params: [
      { name: 'q', in: 'query', type: 'string', required: false, desc: '按代码或现名匹配（LIKE %q%）。为空则返回报告数最多的一批。' },
      { name: 'limit', in: 'query', type: 'integer', required: false, desc: '最大条数。<=0 或 >500 时取默认 200。' },
    ],
    requestExample: `curl -s -G https://portal.example.com/api/symbols \\
  -H "Authorization: Bearer <query-token>" \\
  --data-urlencode "q=宁德" \\
  --data-urlencode "limit=50"`,
    responseExample: `{
  "count": 1,
  "symbols": [
    {"symbol": "300750", "name": "宁德时代", "count": 6, "latest": "2026-07-02"}
  ]
}`,
    errors: [
      { code: 401, when: '无有效 cookie 会话且无 query/all 令牌。`unauthorized`。' },
    ],
    notes: '跨新报告 + 旧 old_meta 聚合每个代码的报告数；latest 为两者最大日期。空代码行跳过。name 取 stocks 表，回退内存名录。按 count DESC, symbol 排序。无必填参数（空 q 返回全部至上限）。',
  },
  {
    method: 'GET',
    path: '/api/tracking',
    scope: 'query',
    summary: '列出某标的的结构化假设/跟踪项，供重跑复核。',
    params: [
      { name: 'symbol', in: 'query', type: 'string', required: true, desc: '股票代码。缺失/空 → 400。' },
      { name: 'status', in: 'query', type: 'string', required: false, desc: '精确状态过滤（如 pending/confirmed/invalidated）。为空返回全部。' },
      { name: 'limit', in: 'query', type: 'integer', required: false, desc: '最大条数。<=0 或 >500 时取默认 100。' },
    ],
    requestExample: `curl -s -G https://portal.example.com/api/tracking \\
  -H "Authorization: Bearer <query-token>" \\
  --data-urlencode "symbol=300750" \\
  --data-urlencode "status=pending"`,
    responseExample: `{
  "symbol": "300750",
  "has": true,
  "count": 1,
  "items": [
    {
      "id": 42,
      "report_uid": "300750|2026-07-02|投资决策|汇总",
      "itype": "assumption",
      "content": "H2 出货量同比 +20%",
      "status": "pending",
      "review_point": "2026-10-31 三季报",
      "created_at": "2026-07-02 08:15:03"
    }
  ]
}`,
    errors: [
      { code: 401, when: '无有效 cookie 会话且无 query/all 令牌。`unauthorized`。' },
      { code: 400, when: 'symbol 缺失/空。`symbol required`。' },
    ],
    notes: '返回 id 用于 PATCH /api/tracking/{id} 单项改状态（复核闭环：读 pending → 改状态）。入库带 tracking 数组会整体覆盖该 uid 的项。按 created_at DESC 排序。report_uid 关联回报告。',
  },
  {
    method: 'PATCH',
    path: '/api/tracking/{id}',
    scope: 'ingest',
    summary: '按 id 更新单个假设/跟踪项的状态或复核点（重跑复核闭环）。',
    params: [
      { name: 'id', in: 'path', type: 'integer', required: true, desc: 'tracking 项 id（来自 GET /api/tracking）。非整数 → 400。' },
      { name: 'status', in: 'body', type: 'string', required: false, desc: '新状态（如 confirmed/invalidated）。为空则不改。' },
      { name: 'review_point', in: 'body', type: 'string', required: false, desc: '新复核点。为空则不改。status 与 review_point 至少给一个。' },
    ],
    requestExample: `curl -s -X PATCH https://portal.example.com/api/tracking/42 \\
  -H "Authorization: Bearer <ingest-token>" \\
  -H "Content-Type: application/json" \\
  -d '{"status":"confirmed","review_point":"三季报已验证"}'`,
    responseExample: `{"ok":true,"id":42,"status":"confirmed"}`,
    errors: [
      { code: 401, when: 'Bearer 缺失/无效或无 ingest/all 作用域。`unauthorized`。' },
      { code: 400, when: 'id 非整数（`bad id`），或 status 与 review_point 都为空（`status or review_point required`）。' },
      { code: 404, when: 'id 未命中任何 tracking 项。`not found`。' },
    ],
    notes: '只改传入的非空字段。不影响其它项，也不会像入库那样整体覆盖。注意：对该报告重新入库（带 tracking 数组）会重建其 tracking 行、id 会变，所以复核请在两次入库之间进行。',
  },
  {
    method: 'DELETE',
    path: '/api/report',
    scope: 'ingest',
    summary: '按 uid 撤回一篇报告（连带其 tracking 项）。',
    params: [
      { name: 'uid', in: 'query', type: 'string', required: true, desc: '报告身份键。缺失/空 → 400。' },
    ],
    requestExample: `curl -s -X DELETE https://portal.example.com/api/report \\
  -H "Authorization: Bearer <ingest-token>" \\
  --data-urlencode "uid=300750|2026-07-02|投资决策|汇总"`,
    responseExample: `{"ok":true,"deleted":1}`,
    errors: [
      { code: 401, when: 'Bearer 缺失/无效或无 ingest/all 作用域。`unauthorized`。' },
      { code: 400, when: 'uid 缺失/空。`uid required`。' },
      { code: 500, when: '数据库删除失败。`database error`。' },
    ],
    notes: '一个事务内删报告 + 其 tracking 项。deleted = 删除的报告行数：未命中返回 deleted:0（非 404），所以重复调用是幂等的。用于清理错代码/重复/测试数据或撤回结论。',
  },
  {
    method: 'GET',
    path: '/healthz',
    scope: '公开（免鉴权）',
    summary: '存活探针，返回版本、commit 与报告计数。',
    params: [],
    requestExample: `curl -s https://portal.example.com/healthz`,
    responseExample: `{"ok":true,"version":"1.4.0","commit":"a1b2c3d","new":1284,"old":50213}`,
    errors: [],
    notes: 'new = 新（入库）报告数；old = 旧门户同步的元数据行数。免令牌/会话；不计入请求日志。',
  },
  {
    method: 'GET',
    path: '/api/version',
    scope: '公开（免鉴权）',
    summary: '构建/版本信息（登录/关于页展示）。',
    params: [],
    requestExample: `curl -s https://portal.example.com/api/version`,
    responseExample: `{"version":"1.4.0","commit":"a1b2c3d","buildDate":"2026-06-28T09:12:00Z"}`,
    errors: [],
    notes: '公开。buildDate 为构建时注入。',
  },
]
