// TS types for the backend JSON contract (aligned with writeJSON in apiui.go).

export interface Me {
  user: string
  name?: string // display name, falls back to username
  admin: boolean
  role?: string
  perms?: Record<string, boolean>
}

// ---- Batch-run feature ----

export interface PluginInput {
  key: string
  label?: string
  required?: boolean
}

export interface PluginConfigField {
  key: string
  label?: string
  secret?: boolean
}

export interface BatchPlugin {
  slug: string
  name: string
  version: string
  source: string
  enabled: boolean
  inputs: PluginInput[]
  config: PluginConfigField[]
}

export interface MarketPlugin {
  slug: string
  name: string
  version: string
  description: string
  installed: boolean
}

// A Dify workflow input field, discovered via /parameters (docs/adr/0006-dify-native.md).
export interface DifyInput {
  variable: string
  label?: string
  type?: string
  required?: boolean
  options?: string[]
}

export interface BatchTarget {
  id: number
  plugin_slug: string
  plugin_name?: string
  name: string
  created_at: string
  inputs?: PluginInput[]
}

// Queue summary for the home banner + drawer (docs/adr/0007-run-analysis-and-scheduling.md).
export interface BatchQueueSummary {
  waiting: number // due, awaiting admission (excludes not-yet-due scheduled)
  running: number
  scheduled: number // 定时 jobs not yet due
  budget: number // jobs allowed to run at once
  reserved: number // slots held for 加急
}

export interface BatchJob {
  id: number
  target_id: number
  status: string
  priority?: string // queue priority level (urgent | normal | other)
  run_at?: string // one-shot scheduled start ("" = ASAP)
  scheduled?: boolean // queued but not yet due (定时, waiting for run_at)
  inputs?: string // first row's inputs as a JSON string (for a 标的 label)
  ahead?: number // for a queued job: how many are ahead of it in the queue
  concurrency: number
  max_retries: number
  total: number
  succeeded: number
  partial: number
  failed: number
  created_by: string
  created_at: string
  started_at: string
  finished_at: string
}

export interface BatchItem {
  id: number
  row_index: number
  inputs: string
  status: string
  attempts: number
  run_id: string
  error: string
  started_at: string
  finished_at: string
}

export interface BatchJobDetail {
  job: BatchJob
  counts: { queued: number; running: number; succeeded: number; partial: number; failed: number }
  running_in_process: boolean
  items: BatchItem[]
}

export interface Webhook {
  id: number
  url: string
  events: string[]
  active: boolean
  created_at: string
  has_secret: boolean
  last_status: number
  last_error: string
  last_delivered_at: string
}

export interface WebhooksResp {
  webhooks: Webhook[]
  events: string[]
}

export interface SymbolInfo {
  symbol: string
  name: string
  count: number
  latest: string
}

export interface Rep {
  rid: string
  uid: string
  title: string
  symbol: string
  name: string // as-of company name (snapshot at ingest)
  curName?: string // current company name; differs after rename / backdoor listing
  date: string
  time?: string // UTC RFC3339 ingest instant; legacy rows are date-only/empty
  kind: string
  rtype: string
  source: string
  md: string
  html: string
}

export interface GroupMember {
  rid: string
  rtype: string
  kind: string
  title: string
}

export interface Group {
  key: string
  symbol: string
  name: string // as-of company name (snapshot)
  curName?: string // current company name; differs after rename / backdoor listing
  title?: string // fallback display title for thematic reports with no stock code/name
  date: string
  kind: string
  kinds: string[]
  src: string // "new" | "old"
  n: number
  members: GroupMember[]
}

export interface ResearchItem {
  rid: string
  title: string
  rtype: string
  date: string
  source: string
}

export interface ResearchResp {
  items: ResearchItem[]
  total: number
  page: number
  pages: number
  size: number
}

export interface HomeResp {
  groups: Group[]
  newTotal: number
  oldTotal: number
  totalRuns: number
  page: number
  pages: number
  size: number
  types: string[]
  links: LinkItem[]
}

export interface TimelineNode {
  date: string
  n: number
}

export interface SubTab {
  rid: string
  label: string
  rtype: string
}

export interface StockResp {
  symbol: string
  name: string
  selDate: string
  selKind: string
  selRID: string
  timeline: TimelineNode[]
  kinds: string[]
  subtabs: SubTab[]
  rep: Rep | null
}

export interface RunResp {
  key: string
  symbol: string
  name: string
  date: string
  selRID: string
  tabs: SubTab[]
  rep: Rep | null
}

export interface LinkItem {
  id: number
  label: string
  url: string
  icon?: string
  newTab?: boolean // open in a new tab (default true)
  ord: number
}

export interface TypeRow {
  name: string
  kind: string
  ord: number
  isSummary: boolean
  label: string
}

export interface TypeGroup {
  kind: string
  rows: TypeRow[]
}

export interface TypesResp {
  groups: TypeGroup[]
  kinds: string[]
}

export interface Role {
  code: string
  name: string
}

export interface UserRow {
  username: string
  role: string
  display_name?: string
  email?: string
  active: boolean
  last_login?: string
  groups: number[] // group ids the user belongs to
}

export interface UserGroupRow {
  id: number
  name: string
  description?: string
  weight: number // 加急 tickets granted per period to each member (ADR 0005)
  priority?: string // default run priority for members (non-urgent tier; ADR 0007)
  members: number // member count
}

// 加急 ticket balance for the batch run form (ADR 0005).
export interface BatchTickets {
  unlimited: boolean
  remaining?: number
  allocation?: number
  period_days?: number
}

export interface UsersResp {
  users: UserRow[]
  me: string
  roles: Role[]
  groups: UserGroupRow[]
}

export interface SettingsResp {
  oldBase: string
  oldUser: string
  hasPass: boolean
  timezone: string // '' = follow system zone
  siteTitle: string
  siteLogoUrl: string
  footerText: string
  footerShowInfo: boolean
  footerShowVersion: boolean
  pwaEnabled: boolean
  pwaIconUrl: string
  newCount: number
}

export interface SiteSettings {
  siteTitle: string
  siteLogoUrl: string
  footerText: string
  footerShowInfo: boolean
  footerShowVersion: boolean
  pwaEnabled: boolean
  pwaIconUrl: string
}

export interface LegacyImportStatus {
  running: boolean
  imported: number
  skipped: number
  failed: number
  aborted: boolean
  error: string
  count: number
  started: string
  finished: string
}

export interface TokenRow {
  id: number
  token: string
  name: string
  scope: string
  created: string
  expires: string
  lastUsed: string
}

// ---- Downloadable iframe apps (docs/adr/0003-downloadable-apps.md) ----

export interface AppSummary {
  id: string
  name: string
  icon?: string
  version?: string
  entry?: string
  scopes?: string[]
}

export interface AppsResp {
  apps: AppSummary[]
}

export interface AppTokenResp {
  app: AppSummary
  token: string
  scopes: string[]
  expires_in: number
}
