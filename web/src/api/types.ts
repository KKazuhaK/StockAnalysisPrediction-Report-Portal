// TS types for the backend JSON contract (aligned with writeJSON in apiui.go).

export interface Me {
  user: string
  admin: boolean
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
  newCount: number
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
