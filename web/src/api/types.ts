// TS types for the backend JSON contract (aligned with writeJSON in apiui.go).

export interface Me {
  user: string
  name?: string // display name, falls back to username
  admin: boolean
  role?: string
  perms?: Record<string, boolean>
  email?: string // the user's email (for the "email me when done" opt-in)
  mail_enabled?: boolean // whether SMTP is configured, so email features can be offered
  // Security state, so the account page can branch before the user submits (ADR 0023).
  federated?: boolean // credentials live at the IdP: no local password, no local second factor
  totp_enabled?: boolean
  passkeys?: number
}

// ---- Batch-run feature ----

export interface PluginInput {
  key: string
  label?: string
  required?: boolean
  // The declared control kind, straight from Dify's /parameters ("text-input" | "paragraph" |
  // "number" | "select" | "file" | "file-list"). Absent on an older server, and on a manifest
  // plugin that declares no kind — both read as a plain text box, which is what the run form
  // drew for everything before this field existed.
  type?: string
  options?: string[] // the allowed values of a "select" input
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

// What the confirm-identity dialog may offer, decided by the server (GET /api/account/stepup/policy).
// The login mode is a deployment policy, so which channels are open is not something the page can
// work out from the account alone: under force-SSO the password box is not drawn at all.
export interface StepUpPolicy {
  password: boolean
  sso: boolean
  providers?: { slug: string; kind: string; name: string; icon?: string }[]
  reason?: string // "sso_required" when the password channel is closed by the login mode
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
  // Surfaces an admin allows this target on. The API always sends the resolved list, so an
  // unset target arrives as all four rather than as an empty array the UI would have to
  // special-case.
  surfaces?: Surface[]
}

// A place a target can be picked. The run queue is not one: it lists jobs that already
// exist and never chooses a target.
export type Surface = 'run' | 'batch' | 'recurring' | 'chat'
export const ALL_SURFACES: Surface[] = ['run', 'batch', 'recurring', 'chat']

// One target's row in the pull-from-Dify preview (POST /api/admin/batch/dify/refresh).
//
// The preview writes nothing. `inputs` is exactly what a confirm would store — and it is the ONLY
// thing a confirm stores: remote_name is shown so an admin can tell the key still points at the
// workflow they think it does, and is never written over the name they chose.
export interface DifyRefreshResult {
  id: number
  local_name: string
  remote_name?: string
  local_mode?: string
  remote_mode?: string
  inputs?: DifyInput[]
  added?: string[]
  removed?: string[]
  required_changed?: string[]
  reordered?: boolean
  changed?: boolean
  name_differs?: boolean
  // Losing the stock-code input silently disables same-day reuse (ADR 0022) rather than failing.
  symbol_input_lost?: string
  error?: string        // the probe failed outright; nothing to apply
  inputs_error?: string // connected, but /parameters did not answer — an empty list is not an answer
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
  // External-tenancy declarations (ADR 0022): the report type this workflow produces, and which
  // input key carries the stock code. Both are required for same-day reuse to apply.
  output_subtype?: string
  symbol_input?: string
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
  id: number
  title: string
  displayTitle: string // title with the as-of company name folded in ("001696 宗申动力 投资决策建议"); server-computed so it matches the MD/PDF download filenames
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
  id: number
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
  id: number
  label: string
  rtype: string
  version?: string // which written form this tab is (ADR 0024)
}

export interface StockResp {
  symbol: string
  name: string
  selDate: string
  selKind: string
  selId: number
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
  selId: number
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
  /** Last authenticated request, throttled server-side. A different fact from last_login. */
  last_seen?: string
  expires_at?: string // account validity cutoff as a panel-tz civil date "YYYY-MM-DD"; "" = never (ADR 0022 R4)
  primary_group: number // primary group id, or 0 when the user inherits the Default group
  federated?: boolean // signs in through an identity provider; has no usable local password
  sso_slug?: string // which provider, when federated
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
  parent_id?: number // where this OU sits in the tree; 0/absent = a root (ADR 0022)
  // External-user tenancy (ADR 0022). restricted is this OU's own flag; restricted_effective also
  // accounts for a restricted ancestor (restriction is sticky down the OU tree).
  restricted?: boolean
  restricted_effective?: boolean
  daily_run_quota?: number | null // run cap for members; null = inherit the parent OU, 0 = unlimited
  /** The window daily_run_quota is measured over: day | week | month | total. '' means day. */
  run_quota_period?: string
}

// Per-OU run allow-list matrix (ADR 0022 R3): which workflows a group may run, on which surfaces.
/** One workflow an OU may be allowed to run, and the surfaces the workflow itself permits. */
export interface GroupTargetRow {
  id: number
  name: string
  surfaces: string[]
  output_subtype?: string
}

export interface GroupTargetsResp {
  granted: { target_id: number; surfaces: string[] }[]
  targets: GroupTargetRow[]
}

// Run-quota balance for the run form (ADR 0022 R2). limited=false for internal users,
// admins, and restricted OUs with no cap — the UI then omits the chip entirely.
export interface RunQuota {
  limited: boolean
  limit?: number
  used?: number
  remaining?: number
  period?: string // the window the cap covers: day | week | month | total ('' = day)
  resets_at?: string // next reset as a UTC instant; localized client-side. '' for a lifetime cap,
  // which never refills — the UI must not invent a date for it.
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
  audit_enabled: boolean
  audit_days: number
  batch_floor: number
  reports_floor: number
  audit_floor: number
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
  audit: number
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
  audit_deleted: number
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
  // Run-form defaults — what the run dialog opens on, edited on its own settings page
  // (Manage → Run defaults). 0 / false means "no default", a supported choice.
  run_default_mode?: RunMode // default mode button: now|preset|scheduled (ADR 0014)
  run_default_idle?: boolean // pre-check "run when queue idle" (immediate mode only)
  run_default_target_id?: number // pre-selected workflow; 0 = none
  run_default_preset_id?: number // pre-picked preset window; 0 = none
  run_default_retries?: number // pre-filled failure retries (0..5)
  run_default_notify?: boolean // pre-tick "email me when done"
  // Not a default but a display choice: does the run form print a preset window's whole rule next
  // to its name, or leave it to the info button beside the picker. Off = just the name.
  run_show_preset_rule?: boolean
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
// Every default is optional on the wire: an older server sends only the first two, and a portal
// whose admin has set none of them sends 0 / false, which both read as "no default".
export interface RunPresetsResp {
  presets: RunPreset[]
  default_mode: RunMode
  default_idle: boolean
  default_target_id?: number // pre-selected workflow; 0 = none
  default_preset_id?: number // pre-picked preset window; 0 = none
  default_retries?: number // pre-filled failure retries
  default_notify?: boolean // pre-tick "email me when done"
  show_preset_rule?: boolean // print a window's whole rule beside its name in the picker
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
  // The portal's canonical origin, used for reset links, SSO redirect/ACS URLs, the WebAuthn
  // relying-party id, registration links and the captcha host check.
  publicUrl: string
  siteTitle: string
  siteLogoUrl: string
  homeMoreStyle: HomeMoreStyle
  footerText: string
  footerShowInfo: boolean
  footerShowVersion: boolean
  pwaEnabled: boolean
  pwaIconUrl: string
  // Retained for one release line with the settings POST that still accepts them; announcements
  // are rows now and nothing reads these. Delete them together (ADR 0025).
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

// The PUBLIC brand payload from GET /api/site — served with no auth, so the login page can paint
// the right title and logo. Announcements deliberately left it (ADR 0025): a per-audience message
// cannot live on an endpoint anonymous visitors can read and poll. See AnnouncementsProvider.
export interface SiteSettings {
  siteTitle: string
  siteLogoUrl: string
  homeMoreStyle: HomeMoreStyle
  footerText: string
  footerShowInfo: boolean
  footerShowVersion: boolean
  pwaEnabled: boolean
  pwaIconUrl: string
}

// Where an announcement shows. 'home' = the home page only (what the single announcement always
// did); 'app' = every page behind the login.
export type AnnouncementScope = 'home' | 'app'

// Who an announcement is for. 'all' = every signed-in account; 'grant' = only the principals in
// announcement_grants. The reader payload never carries this — see Announcement.
export type AnnouncementAudience = 'all' | 'grant'

// One announcement as the READER sees it (GET /api/announcements). It carries no audience, no
// grants and no ord on purpose: which OUs are being addressed is the shape of the org chart.
// endsAt is included so a tab left open in the background — where polling stops — can retire an
// expired banner on its own instead of painting it for hours.
export interface Announcement {
  id: number
  level: AnnouncementLevel
  title: string
  content: string
  popup: boolean
  dismissible: boolean
  scope: AnnouncementScope
  endsAt: string
}

// One announcement as the ADMIN sees it (GET /api/admin/announcements) — the whole row.
export interface AdminAnnouncement extends Announcement {
  ord: number
  enabled: boolean
  audience: AnnouncementAudience
  grants: string[]
  startsAt: string
  createdAt: string
  createdBy: string
  updatedAt: string
}

export interface TokenRow {
  id: number
  prefix: string
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

// A login-page SSO button (ADR 0023). Deliberately minimal — the public endpoint exposes no
// issuer, client id or configuration, only what is needed to render and start a sign-in.
export interface SSOProviderInfo {
  slug: string
  kind: 'saml' | 'oidc'
  name: string
  /** Login-button icon: '' | 'preset:<name>' | a /site-assets/ path. Never a remote URL. */
  icon?: string
}

// Admin view of an SSO provider (ADR 0023). Note what is NOT here: no client secret, no SP
// private key — the server reports only whether each is set.
export interface SSOProviderAdmin {
  id: number
  kind: 'saml' | 'oidc'
  slug: string
  name: string
  enabled: boolean
  provisioning: 'off' | 'jit'
  default_group: number
  default_role: string
  default_expiry_days: number
  allow_admin_role: boolean
  session_hours: number
  issuer: string
  client_id: string
  scopes: string
  has_client_secret: boolean
  redirect_url: string // derived from the public URL; read-only
  idp_metadata_url: string
  idp_entity_id: string
  has_idp_metadata: boolean
  allow_idp_initiated: boolean
  clock_skew_sec: number
  icon?: string
  /** How an unlinked login finds an existing account: '' (identity link only) | 'username' | 'email'. */
  link_by?: string
  sp_entity_id: string // derived; paste into the IdP
  sp_acs_url: string // derived; paste into the IdP
  sp_cert_pem: string
  sp_cert_not_after: string
  has_sp_key: boolean
  attr_upn: string
  attr_email: string
  attr_display: string
  attr_groups: string
  attr_external_id: string
}

export interface SSOProvidersResp {
  providers: SSOProviderAdmin[]
  public_url: string
  /**
   * The SP addresses for a provider that has not been saved yet, keyed by kind. They depend only on
   * the public URL and the default slug, and the setup guide needs them before anything is stored.
   */
  sp_defaults?: Record<string, { sp_entity_id?: string; sp_acs_url?: string; redirect_url?: string }>
}

// One group rule. Order in the array is the contract — first match wins — so `ord` and `id` are
// assigned by the server from the submitted order and are never chosen by the client.
export interface SSORuleRow {
  id: number
  provider_id: number // 0 = applies to every provider
  ord: number
  enabled: boolean
  attr: string
  value: string
  target_role: string // '' = leave the role alone
  target_group: number // 0 = leave the OU alone
  keep_on_miss: boolean
  ci: boolean // compare the value case-insensitively
  note: string
}

export interface SSORulesResp {
  rules: SSORuleRow[]
  // Ids of enabled rules an earlier rule already answers for, so they can never win. Computed
  // server-side because the ordering rule lives there.
  shadowed: number[]
}

// ---- Comparing two editions of the same analysis ----

/** One candidate to diff a report against: another edition of the same symbol + subtype. */
export interface ComparableReport {
  id: number
  date: string
  title: string
  version: string
}

export interface DiffLine {
  op: '+' | '-' | ' '
  text: string
}

/** What happened to one section between the two documents. Matched on the documents' own headings,
 *  so the comparison needs no schema and works across every report type. */
export interface SectionDiff {
  heading: string // '' for the text before the first heading
  level: number
  status: 'same' | 'changed' | 'added' | 'removed'
  lines?: DiffLine[] // present for 'changed' only
}

export interface ReportDiff {
  a: { id: number; title: string; date: string; symbol: string; name: string; rtype: string; version: string }
  b: { id: number; title: string; date: string; symbol: string; name: string; rtype: string; version: string }
  sections: SectionDiff[]
  changed: number
}

// ---- The review queue (tracking items) ----

/** One assumption a report rests on, with the report context needed to judge it. */
export interface TrackingRow {
  id: number
  symbol: string
  name: string
  itype: string
  content: string
  status: string
  review_point: string
  /** A date parsed out of review_point when it holds one; '' otherwise. */
  due: string
  created_at: string
  report_id: number
  report_title: string
  report_date: string
  report_kind: string
  report_type: string
}

export interface TrackingResp {
  items: TrackingRow[]
  total: number
  counts: Record<string, number>
  /** The itype and status values actually present — the ingest contract lets a workflow send any
   *  string, so the filters are built from the data rather than from a fixed list. */
  itypes: string[]
  statuses: string[]
}

// ---- Audit log ----

/** A resolved IP address. Every field is optional: a country-level database fills the
 *  first two, and an address the database does not know fills none. */
export interface GeoLocation {
  country_code?: string
  country?: string
  region?: string
  city?: string
}

export interface AuditEntry {
  id: number
  at: string
  actor: string // '' for a machine caller holding a token
  /** The OU the actor was in AT THE TIME — people move, so this is not a live lookup. */
  actor_ou: number
  action: string
  target_type: string
  target_id: string
  detail: string // JSON
  /** Source address; '' for a writer with no request (CLI, scheduler). */
  ip?: string
  /** Where the address resolves to. Resolved server-side at READ time, never stored. */
  geo?: GeoLocation
}

/** The IP-database download's progress. Never carries the URL: it holds a vendor credential. */
/** What IP database is installed and active. */
export interface GeoStatus {
    enabled: boolean
    /** The admin's chosen file; '' = use the newest automatically. */
    pick?: string
    files?: { file: string; modified?: string; ok: boolean; info?: GeoDBInfo }[]
    dir: string
    file?: string
    loaded: boolean
    modified?: string
    info?: GeoDBInfo
}

export interface GeoDBInfo {
  type?: string
  build_epoch?: number
  granularity?: string
}

export interface GeoDBInfo {
  type?: string
  build_epoch?: number
  granularity?: string
}

export interface GeoUpdateState {
  updating: boolean
  last_error?: string
  last_file?: string
  last_at?: string
  /** Whether a credential is stored. The credential itself is never sent back. */
  has_key: boolean
  auto: boolean
  auto_hours: number
  source: string
  edition?: string
  url?: string
}

export interface AuditResp {
  items: AuditEntry[]
  total: number
  /** The action values actually present, so rows written by an older build still filter. */
  actions: string[]
  ou_names: Record<string, string>
  /** The portal's business timezone; '' means follow the reader's. Stamps are UTC. */
  timezone?: string
  /** Whether an IP database is loaded, and where one goes if not. */
  geo?: GeoStatus
  /**
   * True when a forwarded request arrived from a peer that is not in trusted_proxies — meaning
   * every address in the table is the reverse proxy's, identical for every visitor.
   */
  proxy_hint?: boolean
}
