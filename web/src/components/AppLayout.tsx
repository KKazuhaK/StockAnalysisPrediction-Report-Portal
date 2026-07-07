import { Suspense, useEffect, useState } from 'react'
import { Badge, Button, Divider, FloatButton, Grid, Layout, Popover, Segmented, Select, Space, Spin, Tooltip, theme } from 'antd'
import { AppstoreOutlined, GlobalOutlined, LogoutOutlined, MessageOutlined, SettingOutlined, ThunderboltOutlined, UnorderedListOutlined, UserOutlined, VerticalAlignTopOutlined } from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { usePrefs } from '../prefs'
import { useReaderPrefs } from '../reader'
import { useAuth } from '../auth'
import { SiteLogo, useSite } from '../site'
import { sanitizeFooterHtml } from '../lib/footerHtml'
import { QUEUE_EVENT, RUN_ANALYSIS_EVENT } from '../lib/shortcuts'
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
  // Reading routes are roomier than the default 1240 cap so the reader can center a
  // comfortably-wide column with the timeline in the left gutter; "wide" mode widens more.
  const { wide } = useReaderPrefs()
  const onReader = /^\/(stock|run)\//.test(loc.pathname)
  // The chat page is a fixed full-height app (like a messenger): it fills the viewport
  // below the header, has no page footer, and only its message thread scrolls — so there's
  // a single scrollbar, not one for the page plus one for the thread.
  const onChat = loc.pathname === '/chat'
  const contentMaxWidth = onReader ? (wide ? 1760 : 1440) : 1240
  const [ver, setVer] = useState<{ version: string; commit: string; buildDate: string } | null>(null)
  const [runOpen, setRunOpen] = useState(false)
  const [queueOpen, setQueueOpen] = useState(false)
  const [queue, setQueue] = useState<BatchQueueSummary | null>(null)
  const [showTop, setShowTop] = useState(false)
  const [accountOpen, setAccountOpen] = useState(false)
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
  // Publish the real (wrap-aware) header height so the /manage sticky rail offsets by it
  // instead of assuming a fixed 64px. A ResizeObserver (not just a window-resize listener)
  // keeps it accurate whenever the header itself changes height — wrap/unwrap, font load,
  // content change — so the rail's top never drifts from a stale value.
  useEffect(() => {
    const el = document.getElementById('rp-app-header')
    if (!el) return
    const set = () => document.documentElement.style.setProperty('--rp-header-h', `${el.offsetHeight}px`)
    set()
    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', set)
      return () => window.removeEventListener('resize', set)
    }
    const ro = new ResizeObserver(set)
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  // Light poll for the header queue badge (the drawer refreshes faster when open).
  useEffect(() => {
    if (!canRun) return
    const load = () => api.get<BatchQueueSummary>('/api/admin/batch/queue').then(setQueue).catch(() => {})
    load()
    const id = setInterval(load, 12000)
    return () => clearInterval(id)
  }, [canRun, queueOpen])

  // Run Analysis + the queue live here as a modal/drawer, but an entry-link shortcut (from
  // the home page) needs to open them — it fires a window event we listen for.
  useEffect(() => {
    const openRun = () => setRunOpen(true)
    const openQueue = () => setQueueOpen(true)
    window.addEventListener(RUN_ANALYSIS_EVENT, openRun)
    window.addEventListener(QUEUE_EVENT, openQueue)
    return () => {
      window.removeEventListener(RUN_ANALYSIS_EVENT, openRun)
      window.removeEventListener(QUEUE_EVENT, openQueue)
    }
  }, [])
  const footerText = settings.footerText || title
  const footerHtml = settings.footerText ? sanitizeFooterHtml(settings.footerText) : ''
  const showFooterInfo = settings.footerShowInfo
  const showFooterVersion = settings.footerShowVersion && !!ver
  const showFooter = showFooterInfo || showFooterVersion

  return (
    <Layout style={{ minHeight: onChat ? undefined : '100vh', height: onChat ? '100dvh' : undefined, background: token.colorBgLayout }}>
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
        <div style={{ flex: onHome ? '0 0 auto' : 1, minWidth: compact && !onHome ? '100%' : 0, order: compact ? 2 : 0, display: 'flex' }}>
          {!onHome && (
            <div style={{ width: '100%', maxWidth: compact ? undefined : 420 }}>
              <Omnibox size="middle" />
            </div>
          )}
        </div>

        <Space size={compact ? 6 : 10} wrap style={{ flexShrink: 0, marginLeft: 'auto' }}>
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
              {canRun && (
                <Button icon={<MessageOutlined />} onClick={() => navigate('/chat')} title={t('nav.chat')}>
                  {t('nav.chat')}
                </Button>
              )}
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
          <Popover
            trigger="click"
            placement="bottomRight"
            open={accountOpen}
            onOpenChange={setAccountOpen}
            styles={{ body: { padding: 8 } }}
            content={
              <div style={{ width: 240, maxWidth: '80vw' }}>
                {/* On mobile the primary nav folds in here (the header buttons are hidden). */}
                {compact && (
                  <>
                    {canRun && (
                      <Button
                        type="text"
                        block
                        icon={<MessageOutlined />}
                        style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-start' }}
                        onClick={() => {
                          setAccountOpen(false)
                          navigate('/chat')
                        }}
                      >
                        {t('nav.chat')}
                      </Button>
                    )}
                    <Button
                      type="text"
                      block
                      icon={<AppstoreOutlined />}
                      style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-start' }}
                      onClick={() => {
                        setAccountOpen(false)
                        navigate('/apps')
                      }}
                    >
                      {t('nav.apps')}
                    </Button>
                    {admin && (
                      <Button
                        type="text"
                        block
                        icon={<SettingOutlined />}
                        style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-start' }}
                        onClick={() => {
                          setAccountOpen(false)
                          navigate('/manage')
                        }}
                      >
                        {t('nav.manage')}
                      </Button>
                    )}
                    <Divider style={{ margin: '8px 0' }} />
                  </>
                )}
                <div style={{ fontSize: 12, color: token.colorTextTertiary, margin: '2px 4px 6px' }}>{t('nav.theme')}</div>
                <Segmented
                  block
                  value={mode}
                  onChange={(v) => setMode(v as 'light' | 'dark' | 'auto')}
                  options={[
                    { value: 'light', label: t('theme.light'), icon: <SunIcon /> },
                    { value: 'dark', label: t('theme.dark'), icon: <MoonIcon /> },
                    { value: 'auto', label: t('theme.auto'), icon: <AutoIcon /> },
                  ]}
                />
                <div style={{ fontSize: 12, color: token.colorTextTertiary, margin: '12px 4px 6px' }}>
                  <GlobalOutlined style={{ marginInlineEnd: 6 }} />
                  {t('nav.language')}
                </div>
                <Select
                  value={lang}
                  onChange={(v) => setLang(v)}
                  style={{ width: '100%' }}
                  // Render the dropdown inside the popover so picking an option doesn't
                  // count as an outside click and close the account panel.
                  getPopupContainer={(trigger) => trigger.parentElement as HTMLElement}
                  options={langs.map((l) => ({ value: l.code, label: l.label }))}
                />
                <Divider style={{ margin: '10px 0 8px' }} />
                <Button
                  type="text"
                  block
                  danger
                  icon={<LogoutOutlined />}
                  style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-start' }}
                  onClick={async () => {
                    setAccountOpen(false)
                    await logout()
                    navigate('/login')
                  }}
                >
                  {t('nav.logout')}
                </Button>
              </div>
            }
          >
            <Button type="text" icon={<UserOutlined />} title={name || user || undefined}>
              {!compact && (name || user)}
            </Button>
          </Popover>
        </Space>
      </Header>

      {/* The announcement shows only on the home page, in its own fixed-width band (not
          inside the Content whose max-width flexes with reader "wide" mode). */}
      {onHome && (
        <div style={{ maxWidth: 1240, width: '100%', margin: '0 auto', padding: '0 20px' }}>
          <SiteAnnouncement style={{ marginTop: 24, marginBottom: 0 }} />
        </div>
      )}

      <Content
        style={{
          padding: onChat ? '16px 16px 12px' : onManage ? 0 : '24px 20px',
          maxWidth: onManage ? 'none' : contentMaxWidth,
          width: '100%',
          margin: '0 auto',
          // Chat fills the remaining viewport height and owns its own (single) scroll.
          ...(onChat ? { flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' } : {}),
        }}
      >
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

      {showFooter && !onManage && !onChat && (
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
