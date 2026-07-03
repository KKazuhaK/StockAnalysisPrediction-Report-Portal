import { createContext, useCallback, useContext, useEffect, useMemo, useState, type CSSProperties, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { api } from './api/client'
import type { SiteSettings } from './api/types'
import { BrandIcon } from './components/icons'

const DEFAULT_FAVICON = '/favicon.svg'
const DEFAULT_SETTINGS: SiteSettings = { siteTitle: '', siteLogoUrl: '' }

interface SiteCtx {
  settings: SiteSettings
  title: string
  logoUrl: string
  refresh: () => Promise<SiteSettings>
}

const Ctx = createContext<SiteCtx | null>(null)

function normalizeSettings(s?: Partial<SiteSettings> | null): SiteSettings {
  return {
    siteTitle: (s?.siteTitle ?? '').trim(),
    siteLogoUrl: (s?.siteLogoUrl ?? '').trim(),
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
