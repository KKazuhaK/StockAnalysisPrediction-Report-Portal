import { Card, Menu } from 'antd'
import type { MenuProps } from 'antd'
import {
  ApiOutlined,
  AppstoreAddOutlined,
  AppstoreOutlined,
  ControlOutlined,
  DatabaseOutlined,
  FileTextOutlined,
  GlobalOutlined,
  KeyOutlined,
  LinkOutlined,
  NotificationOutlined,
  TeamOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

// The admin surface is grouped by domain (site / content / access / batch /
// integrations / maintenance) instead of one flat tab bar. Each leaf is its own
// /manage/{key} route. The layout is a plain flex row that wraps: the menu sits
// left on wide screens and stacks above the content when the viewport narrows —
// no matchMedia, so it renders identically under jsdom.
export default function ManageLayout() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const loc = useLocation()
  const active = loc.pathname.split('/')[2] || 'site'

  const items: MenuProps['items'] = [
    {
      type: 'group',
      label: t('nav.group.site'),
      children: [
        { key: 'site', label: t('settings.general'), icon: <GlobalOutlined /> },
        { key: 'announcement', label: t('nav.announcement'), icon: <NotificationOutlined /> },
        { key: 'links', label: t('nav.links'), icon: <LinkOutlined /> },
      ],
    },
    {
      type: 'group',
      label: t('nav.group.content'),
      children: [{ key: 'types', label: t('nav.types'), icon: <AppstoreOutlined /> }],
    },
    {
      type: 'group',
      label: t('nav.group.access'),
      children: [
        { key: 'users', label: t('nav.users'), icon: <TeamOutlined /> },
        { key: 'tokens', label: t('settings.tokens'), icon: <KeyOutlined /> },
      ],
    },
    {
      type: 'group',
      label: t('nav.group.batch'),
      children: [
        { key: 'batch', label: t('nav.batchAdmin'), icon: <ThunderboltOutlined /> },
        { key: 'runqueue', label: t('nav.runQueue'), icon: <ControlOutlined /> },
      ],
    },
    {
      type: 'group',
      label: t('nav.group.integrations'),
      children: [
        { key: 'apps', label: t('nav.appsAdmin'), icon: <AppstoreAddOutlined /> },
        { key: 'webhooks', label: t('nav.webhooks'), icon: <ApiOutlined /> },
        { key: 'apidoc', label: t('settings.apidoc'), icon: <FileTextOutlined /> },
      ],
    },
    {
      type: 'group',
      label: t('nav.group.maintenance'),
      children: [{ key: 'legacy', label: t('settings.legacyTab'), icon: <DatabaseOutlined /> }],
    },
  ]

  return (
    <Card variant="borderless" styles={{ body: { padding: 16 } }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 16, alignItems: 'flex-start' }}>
        <Menu
          mode="inline"
          selectedKeys={[active]}
          onClick={({ key }) => navigate(`/manage/${key}`)}
          items={items}
          style={{ flex: '1 1 200px', maxWidth: 232, minWidth: 180, border: 'none', background: 'transparent' }}
          inlineIndent={16}
        />
        <div style={{ flex: '999 1 360px', minWidth: 0 }}>
          <Outlet />
        </div>
      </div>
    </Card>
  )
}
