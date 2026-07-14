// TS types for the backend JSON contract (aligned with writeJSON in apiui.go).

export interface Me {
  user: string
  name?: string // display name, falls back to username
  admin: boolean
  role?: string
  perms?: Record<string, boolean>
  email?: string // the user's email (for the "email me when done" opt-in)
  mail_enabled?: boolean // whether SMTP is configured, so email features can be offered
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
  mode?: string // Dify app mode: "" / "workflow" / "chat"
  inputs?: PluginInput[]
}

// A Dify target's editable config, returned by GET /api/admin/batch/dify/targets/{id}.
// The api_key is never sent back — has_key only reports whether one is stored.
export interface DifyTargetEdit {
  id: number
  name: string
  base_url: string
  mode?: string // "" / "workflow" / "chat"
  inputs: DifyInput[]
  has_key: boolean
}

// Queue summary for the home banner + drawer (docs/adr/0007-run-analysis-and-scheduling.md).
export interface BatchQueueSummary {
  waiting: number // due, awaiting admission (excludes not-yet-due scheduled)
  running: number // jobs currently admitted (status running)
  running_rows?: number // concurrent runs (rows) executing now — what the run cap governs
  scheduled: number // 定时 jobs not yet due
  budget: number // max concurrent runs (rows) allowed at once
  reserved: number // slots held for urgent runs
  my_priority?: number // the caller's resolved base priority (0..100, ADR 0008)
  done_today?: number // terminal jobs finished today (server-side count; exact under the paginated list)
}

export interface BatchJob {
  id: number
  target_id: number
  status: string
  priority?: string // "urgent" or a base number 0..100 as a string (ADR 0008)
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
  cancelled?: number // rows the operator cancelled individually (terminal, neither ok nor fail)
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
  conversation_id: string // Dify chat/agent reconcile handle (empty for workflow runs)
  task_id: string // Dify task id (server-side stop / tracing)
  error: string
  started_at: string
  finished_at: string
}

// Interactive chat/assistant (docs/adr/0012). A ChatTarget is a Dify chat/agent app;
// a ChatConversation is the portal's thin index row (Dify holds the messages).
export interface ChatTarget {
  id: number
  name: string
  mode: string // Dify app mode (e.g. "agent-chat", "chat")
}

export interface ChatConversation {
  id: number
  target_id: number
  title: string
  starred: boolean // pinned to the top of the user's list
  created_at: string
  updated_at: string
  started: boolean // has a Dify conversation_id yet (i.e. at least one turn sent)
}

export interface ChatTurn {
  query: string // the user's message
  answer: string // the assistant's reply
  created_at: number
}

export interface BatchJobDetail {
  job: BatchJob
  counts: { queued: number; running: number; succeeded: number; partial: number; failed: number; cancelled: number }
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
  time?: string // latest ingest instant in the run (when pushed to the portal; UTC RFC3339)
  kind: string
  kinds: string[]
  src: string // "new" | "old"
  n: number
  members: GroupMember[]
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
  kinds: string[] // 大类 (top-level categories) for the home filter
  links: LinkItem[]
  linkGroups: LinkGroup[] // named, foldable groups of entry buttons
  kindColors: Record<string, string> // 大类 → antd Tag preset color, admin-configured
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
  groupId?: number // the group it belongs to, or 0/undefined = ungrouped (top-level, inline)
  ord: number
  visible?: boolean // shown on the home page (default true); hidden entries stay in the admin list
}

// How a link group renders on the home page.
export type LinkGroupMode = 'row' | 'expand' | 'popover' | 'modal'

export interface LinkGroup {
  id: number
  name: string
  mode: LinkGroupMode // row (own always-visible row) | expand | popover | modal
  showLabel: boolean // show the group name (mainly for row mode)
  icon?: string // icon name shown on the group's trigger button (empty = default folder glyph)
  ord: number
  visible?: boolean // shown on the home page (default true); hidden groups stay in the admin list
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
  colors: Record<string, string> // 大类 → antd Tag preset color, admin-configured
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
  primary_group: number // primary group id, or 0 when the user inherits the Default group
}

export interface UserGroupRow {
  id: number
  name: string
  description?: string
  is_default?: boolean // the fallback group inherited by users with no primary group
  // weight / urgent_unlimited are null when this group inherits the Default group's
  // value (group model B); a value means this group overrides it.
  weight: number | null // urgent tickets granted per period to each member (ADR 0005)
  urgent_unlimited?: boolean | null // members can run urgent jobs without spending tickets
  // Per-group governance (group model B): null = inherit the Default group.
  allow_urgent?: boolean | null // may members use the urgent lane at all
  max_queued?: number | null // cap on active (queued+running) runs per member; 0 = unlimited
  run_window?: string | null // '' = any hour, else 'H1-H2' (panel timezone)
  priority?: string // base run priority 0..100 override ('' / undefined = inherit the system default; ADR 0008)
  members: number // primary-member count
}

// Storage-cleanup console (docs/adr/0017-storage-cleanup.md). Config + last-run summary; retention
// floors clamp the day inputs. freq drives an admin-set daily/weekly/monthly retention pass.
export interface CleanupConfig {
  freq: 'off' | 'daily' | 'weekly' | 'monthly'
  time: string // "HH:MM" panel timezone
  weekday: number // 0=Sun..6=Sat (weekly)
  monthday: number // 1..31 (monthly)
  batch_enabled: boolean
  batch_days: number
  tokens_enabled: boolean
  tokens_grace_days: number
  reports_enabled: boolean
  reports_days: number
  batch_floor: number
  reports_floor: number
  last_run_period: string
  last_result: CleanupResult | null
}

// The outcome of a cleanup pass (also the preview/run response shape).
export interface CleanupResult {
  at: string
  trigger: string // schedule | manual | preview
  dry_run: boolean
  ok: boolean
  error: string
  batch: number
  tokens: number
  reports: number
  duration_ms: number
}

export interface CleanupUsageCategory {
  key: string
  rows: number
  bytes: number
  eligible: number
  oldest: string
  newest: string
}

export interface CleanupUsage {
  db_bytes: number
  categories: CleanupUsageCategory[]
}

// One recorded cleanup pass in the audit history.
export interface CleanupRun {
  id: number
  ran_at: string
  trigger: string
  dry_run: boolean
  ok: boolean
  error: string
  batch_deleted: number
  tokens_deleted: number
  reports_deleted: number
  duration_ms: number
}

export interface BatchConfig {
  max_jobs: number
  reserved_slots: number
  ticket_period_days: number
  default_priority: number
  urgent_enabled?: boolean
  dify_end_user?: string
  dify_poll_seconds?: number // 0 = streaming; >0 = poll the run status every N seconds
  dify_run_timeout_minutes?: number // cap on one run: portal HTTP client + reconcile poll window
  prio_w_base: number
  prio_w_age: number
  prio_w_fair: number
  prio_age_hours: number
  prio_fair_halflife_hours: number
  run_default_mode?: RunMode // run form default button: now|preset|scheduled (ADR 0014)
  run_default_idle?: boolean // pre-check "run when queue idle" (immediate mode only)
}

// Preset low-peak scheduling window (docs/adr/0014-idle-lane-and-preset-windows.md). Which anchor
// fields apply depends on freq: daily uses only time; weekly adds weekday (0=Sun..6=Sat); monthly
// adds day (1..31); yearly adds month (1..12) + day. time is "HH:mm" in the panel timezone.
export type RunMode = 'now' | 'preset' | 'scheduled'
export type RunFreq = 'daily' | 'weekly' | 'monthly' | 'yearly'
export type RunOverrun = 'continue' | 'next' | 'cancel'

export interface RunPresetAnchor {
  weekday?: number
  month?: number
  day?: number
  time: string
}

// One sub-window of a preset; a preset's eligible time is the union of its intervals.
export interface RunPresetInterval {
  start: RunPresetAnchor
  stop: RunPresetAnchor
}

export interface RunPreset {
  id: number
  label: string
  freq: RunFreq
  intervals: RunPresetInterval[]
  on_overrun: RunOverrun
  enabled: boolean
  invert: boolean // true = run OUTSIDE the intervals (they become "do not run" / peak hours)
  ord: number
}

// GET /api/admin/batch/presets — the preset list plus the run-form defaults, in one fetch.
export interface RunPresetsResp {
  presets: RunPreset[]
  default_mode: RunMode
  default_idle: boolean
}

// Urgent ticket balance for the batch run form (ADR 0005).
export interface BatchTickets {
  unlimited: boolean
  remaining?: number
  allocation?: number
  period_days?: number
  urgent_enabled?: boolean // when false, the run forms hide the urgent control entirely
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
  homeMoreStyle: HomeMoreStyle
  footerText: string
  footerShowInfo: boolean
  footerShowVersion: boolean
  pwaEnabled: boolean
  pwaIconUrl: string
  announcementEnabled: boolean
  announcementPopup: boolean
  announcementLevel: AnnouncementLevel
  announcementTitle: string
  announcementContent: string
  newCount: number
}

export type AnnouncementLevel = 'notice' | 'success' | 'warning' | 'error'

// How the home-page "More" button reveals folded quick links.
export type HomeMoreStyle = 'expand' | 'modal' | 'popover'

export interface SiteSettings {
  siteTitle: string
  siteLogoUrl: string
  homeMoreStyle: HomeMoreStyle
  footerText: string
  footerShowInfo: boolean
  footerShowVersion: boolean
  pwaEnabled: boolean
  pwaIconUrl: string
  announcementEnabled: boolean
  announcementPopup: boolean
  announcementLevel: AnnouncementLevel
  announcementTitle: string
  announcementContent: string
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

// One entry in the GitHub-hosted app market index.
export interface AppMarketEntry {
  id: string
  name: string
  icon?: string
  version?: string
  description?: string
  scopes?: string[]
  installed?: boolean
}

export interface AppMarketResp {
  index_url: string
  apps: AppMarketEntry[]
}

// The parse-only response from install?preview=1 (drives the permission prompt).
export interface AppPreviewResp {
  preview: boolean
  app: AppSummary
}

// Recurring tasks (计划任务; docs/adr/0018-recurring-tasks.md): a saved job template + a
// daily/weekly/monthly cadence that the server fires into the run queue.
export interface RecurringTask {
  id: number
  name: string
  target_id: number
  target_name?: string
  concurrency: number
  priority: string // '' (normal) | 'idle'
  max_retries: number
  freq: string // daily | weekly | monthly
  at_time: string // "HH:MM" (panel timezone)
  weekday: number // 0=Sun..6=Sat (weekly)
  monthday: number // 1..31 (monthly)
  enabled: boolean
  created_by: string
  created_at: string
  last_fired: string // YYYY-MM-DD period-stamp of the last fire ("" = never)
  row_count: number
  next_run?: string // computed next fire time (panel tz)
}

// One firing of a task → the batch job it created (the audit/history chain).
export interface RecurringRun {
  id: number
  task_id: number
  job_id: number
  fired_at: string
  job_status?: string
}

// The detail response adds the full template rows + fire history.
export interface RecurringDetail extends RecurringTask {
  rows: Record<string, string>[]
  history: RecurringRun[]
}

export interface RecurringTasksResp {
  tasks: RecurringTask[]
}
