import { Suspense, useEffect, useState } from 'react'
import { Badge, Button, Dropdown, FloatButton, Grid, Layout, Space, Spin, Tooltip, theme } from 'antd'
import { AppstoreOutlined, GlobalOutlined, LogoutOutlined, SettingOutlined, ThunderboltOutlined, UnorderedListOutlined, UserOutlined, VerticalAlignTopOutlined } from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { usePrefs } from '../prefs'
import { useReaderPrefs } from '../reader'
import { useAuth } from '../auth'
import { SiteLogo, useSite } from '../site'
import { sanitizeFooterHtml } from '../lib/footerHtml'
import Omnibox from './Omnibox'
import RunAnalysisModal from './RunAnalysisModal'
import QueueDrawer from './QueueDrawer'
import SiteAnnouncement from './SiteAnnouncement'
import type { BatchQueueSummary } from '../api/types'
import { AutoIcon, MoonIcon, SunIcon } from './icons'

const { Header, Content, Footer } = Layout

export default function AppLayout() {
  const { t } = useTranslation()
  const { settings, title } = useSite()
  const { mode, setMode, lang, setLang, langs } = usePrefs()
  const { user, name, admin, can, logout } = useAuth()
  const navigate = useNavigate()
  const loc = useLocation()
  const { token } = theme.useToken()
  const screens = Grid.useBreakpoint()
  const compact = !screens.md // phone / small tablet
  const onHome = loc.pathname === '/'
  // The admin console runs full-bleed (its own left rail wants the whole width) and
  // suppresses the reader-facing announcement banner, which is noise for an operator.
  const onManage = loc.pathname === '/manage' || loc.pathname.startsWith('/manage/')
  // Reading routes in "wide" mode get a roomier page than the default 1240 cap.
  const { wide } = useReaderPrefs()
  const onReader = /^\/(stock|run)\//.test(loc.pathname)
  const contentMaxWidth = onReader && wide ? 1600 : 1240
  const [ver, setVer] = useState<{ version: string; commit: string; buildDate: string } | null>(null)
  const [runOpen, setRunOpen] = useState(false)
  const [queueOpen, setQueueOpen] = useState(false)
  const [queue, setQueue] = useState<BatchQueueSummary | null>(null)
  const [showTop, setShowTop] = useState(false)
  const canRun = can('run_batch')

  // Show back-to-top once the window has scrolled past ~one screen. Self-controlled
  // (rather than antd's FloatButton.BackTop) so it's reliable across pages.
  useEffect(() => {
    const onScroll = () => setShowTop(window.scrollY > 300)
    window.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
    return () => window.removeEventListener('scroll', onScroll)
  }, [])
  useEffect(() => {
    api.get<{ version: string; commit: string; buildDate: string }>('/api/version').then(setVer).catch(() => {})
  }, [])
  // Publish the real (wrap-aware) header height so the /manage sticky rail can offset by
  // it instead of assuming a fixed 64px — the header grows taller when it wraps on mobile.
  useEffect(() => {
    const measure = () => {
      const el = document.getElementById('rp-app-header')
      if (el) document.documentElement.style.setProperty('--rp-header-h', `${el.offsetHeight}px`)
    }
    measure()
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [])
  // Light poll for the header queue badge (the drawer refreshes faster when open).
  useEffect(() => {
    if (!canRun) return
    const load = () => api.get<BatchQueueSummary>('/api/admin/batch/queue').then(setQueue).catch(() => {})
    load()
    const id = setInterval(load, 12000)
    return () => clearInterval(id)
  }, [canRun, queueOpen])
  const footerText = settings.footerText || title
  const footerHtml = settings.footerText ? sanitizeFooterHtml(settings.footerText) : ''
  const showFooterInfo = settings.footerShowInfo
  const showFooterVersion = settings.footerShowVersion && !!ver
  const showFooter = showFooterInfo || showFooterVersion

  return (
    <Layout style={{ minHeight: '100vh', background: token.colorBgLayout }}>
      <Header
        id="rp-app-header"
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

        {/* On mobile the search drops to its own full-width row (order:2) below the controls.
            On the home page there is no header search, so don't force that empty row —
            otherwise the phantom line pushes the control row off-center in the header. */}
        <div style={{ flex: 1, minWidth: compact && !onHome ? '100%' : 0, order: compact ? 2 : 0, display: 'flex' }}>
          {!onHome && (
            <div style={{ width: '100%', maxWidth: compact ? undefined : 420 }}>
              <Omnibox size="middle" />
            </div>
          )}
        </div>

        <Space size={compact ? 6 : 10} wrap style={{ flexShrink: 0, marginLeft: compact ? 'auto' : 0 }}>
          {canRun && (
            // The primary action keeps its label even on mobile (unlike the other
            // nav buttons, which collapse to icons when compact).
            <Button
              type="primary"
              icon={<ThunderboltOutlined />}
              onClick={() => setRunOpen(true)}
              title={t('nav.runAnalysis')}
            >
              {t('nav.runAnalysis')}
            </Button>
          )}
          {canRun && (
            // Queue glance with a live badge (everything not yet done: running + waiting
            // + scheduled). Icon-only on mobile.
            <Badge count={(queue?.running ?? 0) + (queue?.waiting ?? 0) + (queue?.scheduled ?? 0)} size="small" offset={[-2, 2]}>
              <Button icon={<UnorderedListOutlined />} onClick={() => setQueueOpen(true)} title={t('nav.queue')}>
                {!compact && t('nav.queue')}
              </Button>
            </Badge>
          )}
          {/* On mobile these fold into the account menu below to keep the header light. */}
          {!compact && (
            <>
              <Button icon={<AppstoreOutlined />} onClick={() => navigate('/apps')} title={t('nav.apps')}>
                {t('nav.apps')}
              </Button>
              {admin && (
                <Button icon={<SettingOutlined />} onClick={() => navigate('/manage')} title={t('nav.manage')}>
                  {t('nav.manage')}
                </Button>
              )}
            </>
          )}
          <Dropdown
            trigger={['click']}
            menu={{
              // Theme/language are laid out flat as inline groups (not sideways submenus):
              // the account menu is pinned to the top-right, where a sideways submenu opens
              // awkwardly leftward with a backwards ">" arrow. selectedKeys highlights the
              // active theme + language; multiple lets both stay highlighted at once.
              selectable: true,
              multiple: true,
              selectedKeys: [`theme:${mode}`, `lang:${lang}`],
              items: [
                // On mobile the primary nav lives here (the standalone buttons are hidden).
                ...(compact
                  ? [
                      { key: 'apps', icon: <AppstoreOutlined />, label: t('nav.apps'), onClick: () => navigate('/apps') },
                      ...(admin ? [{ key: 'manage', icon: <SettingOutlined />, label: t('nav.manage'), onClick: () => navigate('/manage') }] : []),
                      { type: 'divider' as const },
                    ]
                  : []),
                {
                  type: 'group' as const,
                  key: 'theme-group',
                  label: t('nav.theme'),
                  children: [
                    { key: 'theme:light', icon: <SunIcon />, label: t('theme.light'), onClick: () => setMode('light') },
                    { key: 'theme:dark', icon: <MoonIcon />, label: t('theme.dark'), onClick: () => setMode('dark') },
                    { key: 'theme:auto', icon: <AutoIcon />, label: t('theme.auto'), onClick: () => setMode('auto') },
                  ],
                },
                {
                  type: 'group' as const,
                  key: 'lang-group',
                  label: t('nav.language'),
                  children: langs.map((l) => ({
                    key: `lang:${l.code}`,
                    icon: <GlobalOutlined />,
                    label: l.label,
                    onClick: () => setLang(l.code),
                  })),
                },
                { type: 'divider' as const },
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
            <Button type="text" icon={<UserOutlined />} title={name || user || undefined}>
              {!compact && (name || user)}
            </Button>
          </Dropdown>
        </Space>
      </Header>

      <Content
        style={{
          padding: onManage ? 0 : '24px 20px',
          maxWidth: onManage ? 'none' : contentMaxWidth,
          width: '100%',
          margin: '0 auto',
          transition: 'max-width 0.2s ease',
        }}
      >
        {!onManage && <SiteAnnouncement style={{ marginBottom: 14 }} />}
        <Suspense fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
          <Outlet />
        </Suspense>
      </Content>

      {canRun && <RunAnalysisModal open={runOpen} onClose={() => setRunOpen(false)} />}
      {canRun && <QueueDrawer open={queueOpen} onClose={() => setQueueOpen(false)} />}

      {/* Back-to-top appears once scrolled down. Data refreshes automatically (queue
          polls; the home list refetches on focus + interval), so no manual refresh. */}
      {showTop && (
        <FloatButton
          icon={<VerticalAlignTopOutlined />}
          tooltip={t('nav.backTop')}
          onClick={() => window.scrollTo({ top: 0, behavior: 'smooth' })}
          style={{ insetInlineEnd: 24, insetBlockEnd: 24 }}
        />
      )}

      {showFooter && (
        <Footer style={{ textAlign: 'center', background: 'transparent', color: token.colorTextTertiary, fontSize: 12 }}>
          <Space size={6} wrap align="center" style={{ justifyContent: 'center' }}>
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
