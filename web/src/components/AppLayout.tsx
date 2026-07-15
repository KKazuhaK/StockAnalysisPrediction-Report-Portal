import { Suspense, useEffect, useMemo, useState } from 'react'
import { Badge, Breadcrumb, Button, Divider, FloatButton, Grid, Layout, Popover, Segmented, Select, Space, Spin, Tooltip, theme } from 'antd'
import { AppstoreOutlined, GlobalOutlined, InfoCircleFilled, LogoutOutlined, MessageOutlined, PlayCircleOutlined, SettingOutlined, UnorderedListOutlined, UserOutlined, VerticalAlignTopOutlined } from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { usePrefs } from '../prefs'
import { useReaderPrefs } from '../reader'
import { useAuth } from '../auth'
import { SiteLogo, useSite } from '../site'
import { sanitizeFooterHtml } from '../lib/footerHtml'
import { QUEUE_EVENT, RUN_ANALYSIS_EVENT } from '../lib/shortcuts'
import { useVersionCheck } from '../lib/useVersionCheck'
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
  // Mobile chat is a focused, messenger-like surface: collapse global navigation chrome so
  // the thread and composer get nearly the full viewport. Desktop keeps the full portal header.
  const chatFocus = compact && onChat
  const contentMaxWidth = onReader ? (wide ? 1760 : 1440) : 1240
  // A back-navigation breadcrumb under the header. Shown on the main pages (apps + children, queue,
  // chat, reader) but not on the home page (nothing to trace) or /manage (it has its own left rail).
  // Ancestors are links; the current page is plain text. Third-party app pages (/apps/x/:id) show
  // Home > Apps with both linked, since the app's own name isn't available in the layout.
  const crumbs = useMemo<{ label: string; to?: string }[]>(() => {
    const p = loc.pathname
    const home = { label: t('nav.home'), to: '/' }
    const apps = { label: t('nav.apps'), to: '/apps' }
    if (p === '/apps') return [home, { label: t('nav.apps') }]
    if (p === '/apps/recurring') return [home, apps, { label: t('nav.recurring') }]
    if (p === '/apps/batch') return [home, apps, { label: t('nav.batch') }]
    if (p.startsWith('/apps/x/')) return [home, apps]
    if (p === '/queue') return [home, { label: t('nav.queue') }]
    if (p === '/chat') return [home, { label: t('nav.chat') }]
    if (p.startsWith('/stock/') || p.startsWith('/run/')) return [home, { label: decodeURIComponent(p.split('/')[2] || '') }]
    return []
  }, [loc.pathname, t])
  const showCrumbs = crumbs.length > 0 && !onManage && !chatFocus
  // Reset the routed Suspense boundary when the top-level page changes, so navigating to a not-yet-
  // loaded chunk shows the spinner at once instead of freezing on the previous page (React 19 + RR7
  // keep the old UI during the transition otherwise). /manage/* collapses to one key — its tabs are
  // nested under ManageLayout, which owns its own Suspense, so its shell must not remount per tab.
  const suspenseKey = loc.pathname.startsWith('/manage') ? '/manage' : loc.pathname
  const [ver, setVer] = useState<{ version: string; commit: string; buildDate: string } | null>(null)
  const [runOpen, setRunOpen] = useState(false)
  const [runTargetId, setRunTargetId] = useState<number | undefined>() // pinned workflow from an entry-button shortcut
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
  // "New version" prompt: once a deploy lands under this open tab, show a persistent inline banner
  // (not a floating notification that overlaps content) with a Refresh action. It stays until refresh
  // so a user cannot accidentally dismiss the only indication that the open client is stale.
  const updateAvailable = useVersionCheck()

  // Warm the report-viewing chunks shortly after the shell mounts. StockPage/RunPage statically pull
  // the heavy Markdown chunk, so pre-loading them makes clicking a report navigate near-instantly
  // instead of waiting on a chunk download (paired with the keyed Suspense below, which shows a
  // spinner if a click still races the download).
  useEffect(() => {
    const timer = window.setTimeout(() => {
      void import('../pages/StockPage')
      void import('../pages/RunPage')
    }, 1000)
    return () => window.clearTimeout(timer)
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
    const openRun = (e: Event) => {
      const detail = (e as CustomEvent).detail as { targetId?: number } | undefined
      setRunTargetId(detail?.targetId)
      setRunOpen(true)
    }
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
        aria-hidden={chatFocus}
        className={chatFocus ? 'rp-app-header rp-app-header--chat-focus' : 'rp-app-header'}
        style={{
          position: 'sticky',
          top: 0,
          zIndex: 20,
          display: chatFocus ? 'none' : 'flex',
          alignItems: 'center',
          flexWrap: chatFocus ? 'nowrap' : 'wrap',
          rowGap: chatFocus ? 0 : 8,
          gap: compact ? 8 : 16,
          height: 'auto',
          minHeight: chatFocus ? 48 : 64,
          // antd's Header sets line-height:64px, which children inherit as a 64px line
          // box — that stretched the wrapped mobile rows and the search box, adding
          // uneven whitespace. Reset it so items size to their content.
          lineHeight: 'normal',
          padding: chatFocus ? '6px 10px' : compact ? '8px 12px' : '0 20px',
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
          {!compact ? title : chatFocus ? t('nav.chat') : null}
        </Link>

        {/* On mobile the search drops to its own full-width row (order:2) below the controls.
            On the home page there is no header search, so don't force that empty row —
            otherwise the phantom line pushes the control row off-center in the header. */}
        <div
          className="rp-header-search"
          style={{
            flex: onHome ? '0 0 auto' : 1,
            minWidth: compact && !onHome && !chatFocus ? '100%' : 0,
            order: compact ? 2 : 0,
            display: chatFocus ? 'none' : 'flex',
          }}
        >
          {!onHome && !chatFocus && (
            <div style={{ width: '100%', maxWidth: compact ? undefined : 420 }}>
              <Omnibox size="middle" />
            </div>
          )}
        </div>

        {/* Vertical gap (14) clears the queue badge's overhang so a 2-digit count (10+)
            doesn't collide with the button on the wrapped row above it. */}
        <Space size={compact ? [8, 14] : 10} wrap style={{ flexShrink: 0, marginLeft: 'auto' }}>
          {canRun && !chatFocus && (
            // The primary action keeps its label even on mobile (unlike the other
            // nav buttons, which collapse to icons when compact).
            <Button
              type="primary"
              icon={<PlayCircleOutlined />}
              onClick={() => {
                setRunTargetId(undefined) // the header button is the generic entry — never inherit a pinned target
                setRunOpen(true)
              }}
              title={t('nav.runAnalysis')}
            >
              {t('nav.runAnalysis')}
            </Button>
          )}
          {canRun && !chatFocus && (
            // Queue glance with a live badge (everything not yet done: running + waiting
            // + scheduled). Icon-only on mobile.
            <Badge count={(queue?.running ?? 0) + (queue?.waiting ?? 0) + (queue?.scheduled ?? 0)} size="small" overflowCount={99} offset={[-4, 3]}>
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
            styles={{ container: { padding: 8 } }}
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

      {/* New-version banner: sticky right under the header. The info-colored bar spans full width, but
          its content aligns to the same content column (contentMaxWidth) as the rest of the page — so on
          an ultra-wide screen the text and buttons stay together instead of stretching to the far edges. */}
      {updateAvailable && (
        <div
          className="rp-update-banner"
          style={{
            position: 'sticky',
            top: 'var(--rp-header-h, 64px)',
            zIndex: 19,
            background: token.colorInfoBg,
          }}
        >
          <div
            style={{
              maxWidth: onChat ? 'none' : contentMaxWidth,
              margin: '0 auto',
              padding: compact ? '8px 12px' : '8px 20px',
              display: 'flex',
              alignItems: 'center',
              gap: 10,
            }}
          >
            <InfoCircleFilled style={{ color: token.colorInfo, fontSize: 15, flexShrink: 0 }} />
            <span style={{ flex: 1, minWidth: 0, color: token.colorText, fontSize: 14 }}>{t('update.desc')}</span>
            <Button type="primary" size="small" onClick={() => window.location.reload()}>
              {t('update.refresh')}
            </Button>
          </div>
        </div>
      )}

      {/* Back-navigation breadcrumb band, aligned to the content column (full-bleed on chat). */}
      {showCrumbs && (
        <div style={{ maxWidth: onChat ? 'none' : contentMaxWidth, width: '100%', margin: '0 auto', padding: compact ? '10px 12px 0' : '14px 20px 0' }}>
          <Breadcrumb items={crumbs.map((c) => ({ title: c.to ? <Link to={c.to}>{c.label}</Link> : c.label }))} />
        </div>
      )}

      {/* The announcement shows only on the home page, in its own fixed-width band (not
          inside the Content whose max-width flexes with reader "wide" mode). */}
      {onHome && (
        <div style={{ maxWidth: 1240, width: '100%', margin: '0 auto', padding: '0 20px' }}>
          <SiteAnnouncement style={{ marginTop: 24, marginBottom: 0 }} />
        </div>
      )}

      <Content
        className={chatFocus ? 'rp-chat-content rp-chat-content--mobile' : onChat ? 'rp-chat-content' : undefined}
        style={{
          // Phones get a slimmer side gutter so reading/content fills more of the narrow
          // screen; desktop keeps the roomier padding.
          padding: chatFocus ? 0 : onChat ? '16px 16px 12px' : onManage ? 0 : compact ? '16px 12px' : '24px 20px',
          // Chat (like the admin console) runs full-bleed — it's a fixed-height app that should
          // use the entire screen width, not the reader's centered 1240 column.
          maxWidth: onManage || onChat ? 'none' : contentMaxWidth,
          width: '100%',
          margin: '0 auto',
          // Chat fills the remaining viewport height and owns its own (single) scroll.
          ...(onChat ? { flex: 1, minHeight: 0, display: 'flex', flexDirection: 'column', overflow: 'hidden' } : {}),
        }}
      >
        {/* Keyed by suspenseKey (above) so a cross-page navigation to a not-yet-loaded chunk shows the
            spinner immediately instead of freezing on the old page. A cached/prefetched route resolves
            synchronously, so this never flashes a spinner for an already-loaded page. */}
        <Suspense key={suspenseKey} fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
          <Outlet />
        </Suspense>
      </Content>

      {canRun && (
        <RunAnalysisModal
          open={runOpen}
          onClose={() => {
            setRunOpen(false)
            setRunTargetId(undefined)
          }}
          initialTargetId={runTargetId}
        />
      )}
      {canRun && <QueueDrawer open={queueOpen} onClose={() => setQueueOpen(false)} />}

      {/* Back-to-top appears once scrolled down. Data refreshes automatically (queue
          polls; the home list refetches on focus + interval), so no manual refresh.
          Not on chat: that page scrolls its own thread and auto-sticks to the newest
          message, and the float button would overlap the composer's send button. */}
      {showTop && !onChat && (
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
