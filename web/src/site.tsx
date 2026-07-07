import { createContext, useCallback, useContext, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from './api/client'
import type { AnnouncementLevel, HomeMoreStyle, SiteSettings } from './api/types'
import { BrandIcon } from './components/icons'

const DEFAULT_FAVICON = '/favicon.svg'
const DEFAULT_SETTINGS: SiteSettings = {
  siteTitle: '',
  siteLogoUrl: '',
  homeMoreStyle: 'expand',
  footerText: '',
  footerShowInfo: true,
  footerShowVersion: true,
  pwaEnabled: true,
  pwaIconUrl: '',
  announcementEnabled: false,
  announcementPopup: false,
  announcementLevel: 'notice',
  announcementTitle: '',
  announcementContent: '',
}

interface SiteCtx {
  settings: SiteSettings
  title: string
  logoUrl: string
  refresh: () => Promise<SiteSettings>
}

const Ctx = createContext<SiteCtx | null>(null)

function normalizeSettings(s?: Partial<SiteSettings> | null): SiteSettings {
  const level = String(s?.announcementLevel ?? '').trim().toLowerCase()
  const moreStyle = String(s?.homeMoreStyle ?? '').trim().toLowerCase()
  return {
    siteTitle: (s?.siteTitle ?? '').trim(),
    siteLogoUrl: (s?.siteLogoUrl ?? '').trim(),
    homeMoreStyle: (['expand', 'modal', 'popover'].includes(moreStyle) ? moreStyle : 'expand') as HomeMoreStyle,
    footerText: (s?.footerText ?? '').trim(),
    footerShowInfo: s?.footerShowInfo !== false,
    footerShowVersion: s?.footerShowVersion !== false,
    pwaEnabled: s?.pwaEnabled !== false,
    pwaIconUrl: (s?.pwaIconUrl ?? '').trim(),
    announcementEnabled: s?.announcementEnabled === true,
    announcementPopup: s?.announcementPopup === true,
    announcementLevel: ['notice', 'success', 'warning', 'error'].includes(level) ? (level as AnnouncementLevel) : 'notice',
    announcementTitle: (s?.announcementTitle ?? '').trim(),
    announcementContent: (s?.announcementContent ?? '').trim(),
  }
}

function faviconLink(): HTMLLinkElement {
  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }
  return link
}

export function SiteProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const [settings, setSettings] = useState<SiteSettings>(DEFAULT_SETTINGS)

  const refresh = useCallback(async () => {
    const next = normalizeSettings(await api.get<SiteSettings>('/api/site'))
    setSettings(next)
    return next
  }, [])

  useEffect(() => {
    refresh().catch(() => setSettings(DEFAULT_SETTINGS))
    // Keep site chrome + the announcement live without a full reload: poll periodically and
    // on tab refocus (same cadence as the home feed's auto-refresh), so an admin's edit
    // reaches everyone shortly — and a changed announcement re-shows its popup.
    const id = setInterval(() => refresh().catch(() => {}), 60000)
    const onVisible = () => {
      if (document.visibilityState === 'visible') refresh().catch(() => {})
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      clearInterval(id)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [refresh])

  const title = settings.siteTitle || t('brand')
  const logoUrl = settings.siteLogoUrl

  useEffect(() => {
    document.title = title
    const link = faviconLink()
    if (logoUrl) {
      link.href = logoUrl
      link.removeAttribute('type')
    } else {
      link.href = DEFAULT_FAVICON
      link.type = 'image/svg+xml'
    }
  }, [title, logoUrl])

  useEffect(() => {
    if (!('serviceWorker' in navigator)) return
    if (settings.pwaEnabled) {
      navigator.serviceWorker.register('/sw.js').catch(() => {})
    } else {
      navigator.serviceWorker.getRegistration('/sw.js').then((reg) => reg?.unregister()).catch(() => {})
    }
  }, [settings.pwaEnabled])

  // Force-update on deploy: each build ships a service worker with a new versioned cache,
  // and once it activates it claims the page — firing controllerchange. If the page was
  // already controlled at load (i.e. this is an update, not the first install), reload once
  // so it runs the fresh build instead of the assets it booted with.
  useEffect(() => {
    if (!('serviceWorker' in navigator)) return
    const hadController = !!navigator.serviceWorker.controller
    let reloaded = false
    const onChange = () => {
      if (!hadController || reloaded) return
      reloaded = true
      window.location.reload()
    }
    navigator.serviceWorker.addEventListener('controllerchange', onChange)
    return () => navigator.serviceWorker.removeEventListener('controllerchange', onChange)
  }, [])

  const value = useMemo<SiteCtx>(() => ({ settings, title, logoUrl, refresh }), [settings, title, logoUrl, refresh])

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useSite(): SiteCtx {
  const c = useContext(Ctx)
  if (!c) throw new Error('useSite must be used within SiteProvider')
  return c
}

export function SiteLogo({
  size = 22,
  color,
  style,
  className,
}: {
  size?: number
  color?: string
  style?: CSSProperties
  className?: string
}) {
  const { logoUrl } = useSite()
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    setFailed(false)
  }, [logoUrl])

  if (logoUrl && !failed) {
    return (
      <img
        src={logoUrl}
        alt=""
        aria-hidden="true"
        className={className}
        onError={() => setFailed(true)}
        style={{
          width: size,
          height: size,
          objectFit: 'contain',
          display: 'inline-block',
          verticalAlign: '-0.15em',
          flexShrink: 0,
          ...style,
        }}
      />
    )
  }

  return <BrandIcon className={className} style={{ color, fontSize: size, flexShrink: 0, ...style }} />
}
