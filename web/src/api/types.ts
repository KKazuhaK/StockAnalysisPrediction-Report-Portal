// TS types for the backend JSON contract (aligned with writeJSON in apiui.go).

export interface Me {
  user: string
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

export interface BatchTarget {
  id: number
  plugin_slug: string
  plugin_name?: string
  name: string
  created_at: string
  inputs?: PluginInput[]
}

export interface BatchJob {
  id: number
  target_id: number
  status: string
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
}

export interface UsersResp {
  users: UserRow[]
  me: string
  roles: Role[]
}

export interface SettingsResp {
  oldBase: string
  oldUser: string
  hasPass: boolean
  timezone: string // '' = follow system zone
  siteTitle: string
  siteLogoUrl: string
  newCount: number
}

export interface SiteSettings {
  siteTitle: string
  siteLogoUrl: string
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
