import { Card, Tabs } from 'antd'
import { ApiOutlined, AppstoreAddOutlined, AppstoreOutlined, ControlOutlined, KeyOutlined, LinkOutlined, TeamOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

export default function ManageLayout() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const loc = useLocation()
  const active = loc.pathname.split('/')[2] || 'links'

  return (
    <Card>
      <Tabs
        activeKey={active}
        onChange={(k) => navigate(`/manage/${k}`)}
        items={[
          { key: 'links', label: t('nav.links'), icon: <LinkOutlined /> },
          { key: 'types', label: t('nav.types'), icon: <AppstoreOutlined /> },
          { key: 'users', label: t('nav.users'), icon: <TeamOutlined /> },
          { key: 'settings', label: t('nav.settings'), icon: <KeyOutlined /> },
          { key: 'batch', label: t('nav.batchAdmin'), icon: <ThunderboltOutlined /> },
          { key: 'runqueue', label: t('nav.runQueue'), icon: <ControlOutlined /> },
          { key: 'apps', label: t('nav.appsAdmin'), icon: <AppstoreAddOutlined /> },
          { key: 'webhooks', label: t('nav.webhooks'), icon: <ApiOutlined /> },
        ]}
      />
      <div style={{ paddingTop: 8 }}>
        <Outlet />
      </div>
    </Card>
  )
}
