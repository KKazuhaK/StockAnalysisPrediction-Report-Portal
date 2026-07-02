import type { ComponentType } from 'react'
import {
  ApiOutlined,
  AppstoreOutlined,
  BankOutlined,
  BarChartOutlined,
  BookOutlined,
  CloudOutlined,
  DatabaseOutlined,
  DollarOutlined,
  FileTextOutlined,
  FundOutlined,
  GithubOutlined,
  GlobalOutlined,
  LineChartOutlined,
  LinkOutlined,
  MailOutlined,
  ReadOutlined,
  RobotOutlined,
  StockOutlined,
} from '@ant-design/icons'

// Curated icon set the admin can pick for a quick-link. Keys are the stable
// names persisted in the DB (links.icon); values are the antd components.
export const LINK_ICON_MAP: Record<string, ComponentType<{ style?: React.CSSProperties }>> = {
  link: LinkOutlined,
  global: GlobalOutlined,
  github: GithubOutlined,
  file: FileTextOutlined,
  book: BookOutlined,
  read: ReadOutlined,
  database: DatabaseOutlined,
  bar: BarChartOutlined,
  line: LineChartOutlined,
  fund: FundOutlined,
  stock: StockOutlined,
  dollar: DollarOutlined,
  bank: BankOutlined,
  mail: MailOutlined,
  api: ApiOutlined,
  robot: RobotOutlined,
  cloud: CloudOutlined,
  app: AppstoreOutlined,
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
