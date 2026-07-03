import { Suspense, useEffect, useState } from 'react'
import { Button, Dropdown, Grid, Layout, Space, Spin, Tooltip, theme } from 'antd'
import { AppstoreOutlined, FileSearchOutlined, GlobalOutlined, LogoutOutlined, SettingOutlined, UserOutlined } from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { usePrefs } from '../prefs'
import { useReaderPrefs } from '../reader'
import { useAuth } from '../auth'
import { SiteLogo, useSite } from '../site'
import { sanitizeFooterHtml } from '../lib/footerHtml'
import Omnibox from './Omnibox'
import { AutoIcon, MoonIcon, SunIcon } from './icons'

const { Header, Content, Footer } = Layout

export default function AppLayout() {
  const { t } = useTranslation()
  const { settings, title } = useSite()
  const { mode, setMode, lang, setLang, langs } = usePrefs()
  const { user, name, admin, logout } = useAuth()
  const navigate = useNavigate()
  const loc = useLocation()
  const { token } = theme.useToken()
  const screens = Grid.useBreakpoint()
  const compact = !screens.md // phone / small tablet
  const onHome = loc.pathname === '/'
  // Reading routes in "wide" mode get a roomier page than the default 1240 cap.
  const { wide } = useReaderPrefs()
  const onReader = /^\/(stock|run)\//.test(loc.pathname)
  const contentMaxWidth = onReader && wide ? 1600 : 1240
  const [ver, setVer] = useState<{ version: string; commit: string; buildDate: string } | null>(null)
  useEffect(() => {
    api.get<{ version: string; commit: string; buildDate: string }>('/api/version').then(setVer).catch(() => {})
  }, [])
  const footerText = settings.footerText || title
  const footerHtml = settings.footerText ? sanitizeFooterHtml(settings.footerText) : ''
  const showFooterInfo = settings.footerShowInfo
  const showFooterVersion = settings.footerShowVersion && !!ver
  const showFooter = showFooterInfo || showFooterVersion

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
          // antd's Header sets line-height:64px, which children inherit as a 64px line
          // box — that stretched the wrapped mobile rows and the search box, adding
          // uneven whitespace. Reset it so items size to their content.
          lineHeight: 'normal',
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
          <SiteLogo size={22} color={token.colorPrimary} />
          {!compact && title}
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
          <Button
            icon={<FileSearchOutlined />}
            onClick={() => navigate('/research')}
            title={t('nav.research')}
          >
            {!compact && t('nav.research')}
          </Button>
          <Button
            icon={<AppstoreOutlined />}
            onClick={() => navigate('/apps')}
            title={t('nav.apps')}
          >
            {!compact && t('nav.apps')}
          </Button>
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
            trigger={['click']}
            menu={{
              items: [
                {
                  key: 'theme',
                  icon: mode === 'light' ? <SunIcon /> : mode === 'dark' ? <MoonIcon /> : <AutoIcon />,
                  label: t('nav.theme'),
                  children: [
                    { key: 'light', icon: <SunIcon />, label: t('theme.light'), onClick: () => setMode('light') },
                    { key: 'dark', icon: <MoonIcon />, label: t('theme.dark'), onClick: () => setMode('dark') },
                    { key: 'auto', icon: <AutoIcon />, label: t('theme.auto'), onClick: () => setMode('auto') },
                  ],
                },
                {
                  key: 'language',
                  icon: <GlobalOutlined />,
                  label: t('nav.language'),
                  children: langs.map((l) => ({
                    key: l.code,
                    label: l.label,
                    onClick: () => setLang(l.code),
                  })),
                },
                { type: 'divider' },
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
              {name || user}
            </Button>
          </Dropdown>
        </Space>
      </Header>

      <Content style={{ padding: '24px 20px', maxWidth: contentMaxWidth, width: '100%', margin: '0 auto', transition: 'max-width 0.2s ease' }}>
        <Suspense fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
          <Outlet />
        </Suspense>
      </Content>

      {showFooter && (
        <Footer style={{ textAlign: 'center', background: 'transparent', color: token.colorTextTertiary, fontSize: 12 }}>
          <Space size={6} wrap style={{ justifyContent: 'center' }}>
            {showFooterInfo && (
              <span style={{ display: 'inline-flex', alignItems: 'center' }}>
                <SiteLogo size={14} style={{ marginInlineEnd: 6 }} />
                {settings.footerText ? <span dangerouslySetInnerHTML={{ __html: footerHtml }} /> : footerText}
              </span>
            )}
            {showFooterInfo && showFooterVersion && <span>·</span>}
            {showFooterVersion && (
              <Tooltip
                title={
                  <div style={{ lineHeight: 1.6, fontWeight: 600 }}>
                    <div>{ver.version}</div>
                    <div>commit: {ver.commit}</div>
                    <div>built: {ver.buildDate}</div>
                  </div>
                }
              >
                <span style={{ cursor: 'help', fontVariantNumeric: 'tabular-nums' }}>{ver.version}</span>
              </Tooltip>
            )}
          </Space>
        </Footer>
      )}
    </Layout>
  )
}
