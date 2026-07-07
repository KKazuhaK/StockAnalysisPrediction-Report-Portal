import { Suspense, useEffect, useState } from 'react'
import { Button, Drawer, Menu, Spin, Tooltip, Typography, theme } from 'antd'
import type { MenuProps } from 'antd'
import {
  ApiOutlined,
  AppstoreAddOutlined,
  AppstoreOutlined,
  ControlOutlined,
  FileTextOutlined,
  GlobalOutlined,
  KeyOutlined,
  LinkOutlined,
  MailOutlined,
  MessageOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  NotificationOutlined,
  PlayCircleOutlined,
  TeamOutlined,
} from '@ant-design/icons'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'

const COLLAPSE_KEY = 'rp.manage.sider.collapsed'
const NARROW_QUERY = '(max-width: 767px)'
// The sticky rail offsets by the real header height, which AppLayout publishes as a
// CSS var (the header wraps taller in compact mode); 64px is the single-row fallback.
const HEADER_VAR = 'var(--rp-header-h, 64px)'

// The admin surface is a full-bleed console: a domain-grouped left rail (collapsible
// to an icon strip, with the choice remembered) beside the active page. Grouping —
// site / content / access / batch / integrations — replaces the old flat tab bar; each
// leaf is its own /manage/{key} route. On a narrow viewport the rail stacks above the
// content (expanded, full width) instead of crushing it. matchMedia is read directly
// (not Grid), and the test shim reports desktop, so jsdom renders identically.
export default function ManageLayout() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const loc = useLocation()
  const { token } = theme.useToken()
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return localStorage.getItem(COLLAPSE_KEY) === '1'
    } catch {
      return false
    }
  })
  const [narrow, setNarrow] = useState(() => {
    try {
      return window.matchMedia(NARROW_QUERY).matches
    } catch {
      return false
    }
  })
  const [ver, setVer] = useState<{ version: string; commit: string; buildDate: string } | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const active = loc.pathname.split('/')[2] || 'site'

  useEffect(() => {
    api.get<{ version: string; commit: string; buildDate: string }>('/api/version').then(setVer).catch(() => {})
  }, [])

  useEffect(() => {
    let mq: MediaQueryList
    try {
      mq = window.matchMedia(NARROW_QUERY)
    } catch {
      return
    }
    const onChange = () => setNarrow(mq.matches)
    mq.addEventListener?.('change', onChange)
    return () => mq.removeEventListener?.('change', onChange)
  }, [])

  const toggle = () => {
    setCollapsed((c) => {
      const next = !c
      try {
        localStorage.setItem(COLLAPSE_KEY, next ? '1' : '0')
      } catch {
        /* private mode / storage disabled — collapse just won't persist */
      }
      return next
    })
  }

  // On a narrow viewport the rail is always expanded (it stacks above the content, so
  // an icon-only strip in a full-width bar would look broken).
  const railCollapsed = narrow ? false : collapsed

  const items: MenuProps['items'] = [
    {
      type: 'group',
      label: t('nav.group.site'),
      children: [
        { key: 'site', label: t('settings.general'), icon: <GlobalOutlined /> },
        { key: 'announcement', label: t('nav.announcement'), icon: <NotificationOutlined /> },
        { key: 'email', label: t('nav.email'), icon: <MailOutlined /> },
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
        { key: 'batch', label: t('nav.batchAdmin'), icon: <PlayCircleOutlined /> },
        { key: 'runqueue', label: t('nav.runQueue'), icon: <ControlOutlined /> },
        { key: 'assistant', label: t('nav.chat'), icon: <MessageOutlined /> },
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
  ]

  // The nav menu, shared by the desktop rail and the mobile drawer. On mobile a click also
  // closes the drawer (so it behaves like a page switch, not a persistent sidebar).
  const menu = (
    <Menu
      mode="inline"
      inlineCollapsed={railCollapsed}
      selectedKeys={[active]}
      onClick={({ key }) => {
        navigate(`/manage/${key}`)
        setDrawerOpen(false)
      }}
      items={items}
      style={{ border: 'none', background: 'transparent' }}
    />
  )

  // The label of the current leaf, for the mobile top bar (so you can see where you are
  // without opening the drawer).
  const leaves = items.flatMap((g) => (g && 'children' in g ? g.children ?? [] : []))
  const activeLeaf = leaves.find((l) => l && 'key' in l && l.key === active)
  const activeLabel = (activeLeaf && 'label' in activeLeaf ? (activeLeaf.label as string) : undefined) ?? t('nav.manage')

  // Mobile: the grouped rail is too tall to stack above the content — fold it into a drawer
  // opened from a slim top bar, so the active page is visible immediately.
  if (narrow) {
    return (
      <div style={{ display: 'flex', flexDirection: 'column', minHeight: `calc(100dvh - ${HEADER_VAR})`, background: token.colorBgContainer }}>
        <div
          style={{
            position: 'sticky',
            top: HEADER_VAR,
            zIndex: 5,
            display: 'flex',
            alignItems: 'center',
            gap: 10,
            padding: '10px 12px',
            background: token.colorBgContainer,
            borderBottom: `1px solid ${token.colorBorderSecondary}`,
          }}
        >
          <Button icon={<MenuUnfoldOutlined />} onClick={() => setDrawerOpen(true)}>
            {activeLabel}
          </Button>
        </div>
        <Drawer
          open={drawerOpen}
          onClose={() => setDrawerOpen(false)}
          placement="left"
          width={260}
          title={t('nav.manage')}
          styles={{ body: { padding: 8 } }}
        >
          {menu}
        </Drawer>
        <div style={{ flex: '1 1 auto', minWidth: 0, padding: 16 }}>
          <Suspense fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
            <Outlet />
          </Suspense>
        </div>
      </div>
    )
  }

  return (
    <div
      style={{
        display: 'flex',
        flexDirection: narrow ? 'column' : 'row',
        alignItems: narrow ? 'stretch' : 'flex-start',
        minHeight: narrow ? undefined : `calc(100dvh - ${HEADER_VAR})`,
        background: token.colorBgContainer,
      }}
    >
      <div
        style={{
          flex: narrow ? '0 0 auto' : `0 0 ${collapsed ? 80 : 236}px`,
          width: narrow ? '100%' : collapsed ? 80 : 236,
          display: 'flex',
          flexDirection: 'column',
          borderInlineEnd: narrow ? undefined : `1px solid ${token.colorBorderSecondary}`,
          borderBottom: narrow ? `1px solid ${token.colorBorderSecondary}` : undefined,
          position: narrow ? 'static' : 'sticky',
          top: narrow ? undefined : HEADER_VAR,
          height: narrow ? 'auto' : `calc(100dvh - ${HEADER_VAR})`,
          transition: 'flex-basis .2s ease, width .2s ease',
        }}
      >
        <div style={{ flex: '1 1 auto', overflowY: 'auto', overflowX: 'hidden', paddingTop: 8 }}>{menu}</div>
        {!narrow && (
          <div
            style={{
              flex: '0 0 auto',
              borderTop: `1px solid ${token.colorBorderSecondary}`,
              padding: 8,
              display: 'flex',
              alignItems: 'center',
              gap: 8,
              justifyContent: collapsed ? 'center' : 'space-between',
            }}
          >
            <Button
              type="text"
              aria-label={t('nav.collapse')}
              onClick={toggle}
              icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
              style={{ display: 'flex', alignItems: 'center', justifyContent: collapsed ? 'center' : 'flex-start' }}
            >
              {!collapsed && <span style={{ marginInlineStart: 8 }}>{t('nav.collapse')}</span>}
            </Button>
            {!collapsed && ver && (
              <Tooltip
                title={
                  <div style={{ lineHeight: 1.6, fontWeight: 600 }}>
                    <div>{ver.version}</div>
                    <div>commit: {ver.commit}</div>
                    <div>built: {ver.buildDate}</div>
                  </div>
                }
              >
                <Typography.Text
                  type="secondary"
                  style={{ fontSize: 12, fontVariantNumeric: 'tabular-nums', cursor: 'help', paddingInlineEnd: 4 }}
                >
                  {ver.version}
                </Typography.Text>
              </Tooltip>
            )}
          </div>
        )}
      </div>
      <div style={{ flex: '1 1 auto', minWidth: 0, padding: narrow ? '16px' : '20px 24px' }}>
        <Suspense fallback={<div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}><Spin size="large" /></div>}>
          <Outlet />
        </Suspense>
      </div>
    </div>
  )
}
