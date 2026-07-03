import { Suspense, useEffect, useState } from 'react'
import { Button, Dropdown, Grid, Layout, Segmented, Select, Space, Spin, theme } from 'antd'
import { AppstoreOutlined, FileSearchOutlined, LogoutOutlined, SettingOutlined, UserOutlined } from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { usePrefs } from '../prefs'
import { useAuth } from '../auth'
import Omnibox from './Omnibox'
import { AutoIcon, BrandIcon, MoonIcon, SunIcon } from './icons'

const { Header, Content, Footer } = Layout

export default function AppLayout() {
  const { t } = useTranslation()
  const { mode, setMode, lang, setLang, langs } = usePrefs()
  const { user, admin, can, logout } = useAuth()
  const navigate = useNavigate()
  const loc = useLocation()
  const { token } = theme.useToken()
  const screens = Grid.useBreakpoint()
  const compact = !screens.md // phone / small tablet
  const onHome = loc.pathname === '/'
  const [ver, setVer] = useState<{ version: string; commit: string; buildDate: string } | null>(null)
  useEffect(() => {
    api.get<{ version: string; commit: string; buildDate: string }>('/api/version').then(setVer).catch(() => {})
  }, [])

  return (
    <Layout style={{ minHeight: '100vh', background: token.colorBgLayout }}>
      <Header
        style={{
          position: 'sticky',
          top: 0,
          zIndex: 20,
          display: 'flex',
          alignItems: 'center',
          flexWrap: 'wrap',
          rowGap: 8,
          gap: compact ? 8 : 16,
          height: 'auto',
          minHeight: 64,
          padding: compact ? '8px 12px' : '0 20px',
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
          {!compact && t('brand')}
        </Link>

        {/* On mobile the search drops to its own full-width row (order:2) below the controls. */}
        <div style={{ flex: 1, minWidth: compact ? '100%' : 0, order: compact ? 2 : 0, display: 'flex' }}>
          {!onHome && (
            <div style={{ width: '100%', maxWidth: compact ? undefined : 420 }}>
              <Omnibox size="middle" />
            </div>
          )}
        </div>

        <Space size={compact ? 6 : 10} wrap style={{ flexShrink: 0, marginLeft: compact ? 'auto' : 0 }}>
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
            value={lang}
            onChange={setLang}
            style={{ width: compact ? 104 : 116 }}
            options={langs.map((l) => ({ value: l.code, label: l.label }))}
          />
          <Button
            icon={<FileSearchOutlined />}
            onClick={() => navigate('/research')}
            title={t('nav.research')}
          >
            {!compact && t('nav.research')}
          </Button>
          {can('run_batch') && (
            <Button
              icon={<AppstoreOutlined />}
              onClick={() => navigate('/apps')}
              title={t('nav.apps')}
            >
              {!compact && t('nav.apps')}
            </Button>
          )}
          {admin && (
            <Button
              icon={<SettingOutlined />}
              onClick={() => navigate('/manage')}
              title={t('nav.manage')}
            >
              {!compact && t('nav.manage')}
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
        <Suspense fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
          <Outlet />
        </Suspense>
      </Content>

      <Footer style={{ textAlign: 'center', background: 'transparent', color: token.colorTextTertiary, fontSize: 12 }}>
        <BrandIcon style={{ marginInlineEnd: 6 }} />
        {t('brand')}
        {ver && ` · ${ver.version} (${ver.commit})`}
      </Footer>
    </Layout>
  )
}
