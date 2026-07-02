import { Button, Dropdown, Layout, Segmented, Select, Space, theme } from 'antd'
import { LogoutOutlined, SettingOutlined, UserOutlined } from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { usePrefs } from '../prefs'
import { useAuth } from '../auth'
import Omnibox from './Omnibox'
import { AutoIcon, BrandIcon, MoonIcon, SunIcon } from './icons'

const { Header, Content } = Layout

export default function AppLayout() {
  const { t } = useTranslation()
  const { mode, setMode, locale, setLocale } = usePrefs()
  const { user, admin, logout } = useAuth()
  const navigate = useNavigate()
  const loc = useLocation()
  const { token } = theme.useToken()
  const onHome = loc.pathname === '/'

  return (
    <Layout style={{ minHeight: '100vh', background: token.colorBgLayout }}>
      <Header
        style={{
          position: 'sticky',
          top: 0,
          zIndex: 20,
          display: 'flex',
          alignItems: 'center',
          gap: 16,
          padding: '0 20px',
          background: token.colorBgContainer,
          borderBottom: `1px solid ${token.colorBorderSecondary}`,
        }}
      >
        <Link
          to="/"
          style={{
            fontSize: 18,
            fontWeight: 700,
            color: token.colorText,
            whiteSpace: 'nowrap',
            display: 'inline-flex',
            alignItems: 'center',
            gap: 8,
          }}
        >
          <BrandIcon style={{ color: token.colorPrimary, fontSize: 22 }} />
          {t('brand')}
        </Link>

        <div style={{ flex: 1, minWidth: 0, display: 'flex' }}>
          {!onHome && (
            <div style={{ width: '100%', maxWidth: 420 }}>
              <Omnibox size="middle" />
            </div>
          )}
        </div>

        <Space size={10} wrap style={{ flexShrink: 0 }}>
          <Segmented
            value={mode}
            onChange={(v) => setMode(v as any)}
            options={[
              { value: 'light', icon: <SunIcon />, title: t('theme.light') },
              { value: 'dark', icon: <MoonIcon />, title: t('theme.dark') },
              { value: 'auto', icon: <AutoIcon />, title: t('theme.auto') },
            ]}
          />
          <Select
            size="middle"
            value={locale}
            onChange={(v) => setLocale(v)}
            style={{ width: 92 }}
            options={[
              { value: 'zh', label: '中文' },
              { value: 'en', label: 'EN' },
            ]}
          />
          {admin && (
            <Button icon={<SettingOutlined />} onClick={() => navigate('/manage')}>
              {t('nav.manage')}
            </Button>
          )}
          <Dropdown
            menu={{
              items: [
                {
                  key: 'logout',
                  icon: <LogoutOutlined />,
                  label: t('nav.logout'),
                  onClick: async () => {
                    await logout()
                    navigate('/login')
                  },
                },
              ],
            }}
          >
            <Button type="text" icon={<UserOutlined />}>
              {user}
            </Button>
          </Dropdown>
        </Space>
      </Header>

      <Content style={{ padding: '24px 20px', maxWidth: 1240, width: '100%', margin: '0 auto' }}>
        <Outlet />
      </Content>
    </Layout>
  )
}
