import type { ComponentType } from 'react'
import {
  AccountBookOutlined,
  ApiOutlined,
  AppstoreOutlined,
  AreaChartOutlined,
  AuditOutlined,
  BankOutlined,
  BarChartOutlined,
  BookOutlined,
  BulbOutlined,
  CalendarOutlined,
  ClockCircleOutlined,
  CloudOutlined,
  CodeOutlined,
  CreditCardOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  DeploymentUnitOutlined,
  DollarOutlined,
  DotChartOutlined,
  ExperimentOutlined,
  FallOutlined,
  FileTextOutlined,
  FireOutlined,
  FlagOutlined,
  FundOutlined,
  GithubOutlined,
  GlobalOutlined,
  GoldOutlined,
  HeatMapOutlined,
  LineChartOutlined,
  LinkOutlined,
  MailOutlined,
  NotificationOutlined,
  PayCircleOutlined,
  PieChartOutlined,
  ProfileOutlined,
  RadarChartOutlined,
  ReadOutlined,
  RiseOutlined,
  RobotOutlined,
  RocketOutlined,
  SafetyCertificateOutlined,
  SearchOutlined,
  SnippetsOutlined,
  SolutionOutlined,
  StarOutlined,
  StockOutlined,
  TeamOutlined,
  ThunderboltOutlined,
  TransactionOutlined,
  TrophyOutlined,
  WalletOutlined,
} from '@ant-design/icons'

// Curated icon set the admin can pick for a quick-link. Keys are the stable
// names persisted in the DB (links.icon); values are the antd components.
// Grouped by theme so the picker reads in a sensible order.
export const LINK_ICON_MAP: Record<string, ComponentType<{ style?: React.CSSProperties }>> = {
  // general / web
  link: LinkOutlined,
  global: GlobalOutlined,
  github: GithubOutlined,
  // documents / research
  file: FileTextOutlined,
  book: BookOutlined,
  read: ReadOutlined,
  profile: ProfileOutlined,
  audit: AuditOutlined,
  solution: SolutionOutlined,
  snippets: SnippetsOutlined,
  search: SearchOutlined,
  // data / charts
  database: DatabaseOutlined,
  bar: BarChartOutlined,
  line: LineChartOutlined,
  area: AreaChartOutlined,
  pie: PieChartOutlined,
  radar: RadarChartOutlined,
  dot: DotChartOutlined,
  heatmap: HeatMapOutlined,
  dashboard: DashboardOutlined,
  fund: FundOutlined,
  // market / trend
  stock: StockOutlined,
  rise: RiseOutlined,
  fall: FallOutlined,
  // finance / money
  dollar: DollarOutlined,
  bank: BankOutlined,
  wallet: WalletOutlined,
  account: AccountBookOutlined,
  transaction: TransactionOutlined,
  gold: GoldOutlined,
  pay: PayCircleOutlined,
  card: CreditCardOutlined,
  // communication
  mail: MailOutlined,
  notification: NotificationOutlined,
  // tech / AI
  api: ApiOutlined,
  robot: RobotOutlined,
  cloud: CloudOutlined,
  app: AppstoreOutlined,
  code: CodeOutlined,
  deployment: DeploymentUnitOutlined,
  experiment: ExperimentOutlined,
  bulb: BulbOutlined,
  thunderbolt: ThunderboltOutlined,
  rocket: RocketOutlined,
  // status / misc
  fire: FireOutlined,
  star: StarOutlined,
  flag: FlagOutlined,
  trophy: TrophyOutlined,
  calendar: CalendarOutlined,
  clock: ClockCircleOutlined,
  team: TeamOutlined,
  safety: SafetyCertificateOutlined,
}

// DEFAULT_LINK_ICON is used when a link has no icon set or an unknown name.
export const DEFAULT_LINK_ICON = 'link'

// linkIconComponent resolves a stored icon name to its component, falling back
// to the default link glyph for empty/unknown names.
export function linkIconComponent(name?: string): ComponentType<{ style?: React.CSSProperties }> {
  return (name && LINK_ICON_MAP[name]) || LINK_ICON_MAP[DEFAULT_LINK_ICON]
}

// LINK_ICON_OPTIONS feeds the admin picker (one entry per mapped icon).
export const LINK_ICON_OPTIONS = Object.keys(LINK_ICON_MAP).map((value) => ({ value }))
