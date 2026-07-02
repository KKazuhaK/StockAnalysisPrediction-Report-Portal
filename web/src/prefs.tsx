import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import i18n, { LOCALE_KEY } from './i18n'

export type ThemeMode = 'light' | 'dark' | 'auto'
export type Locale = 'zh' | 'en'

const THEME_KEY = 'rp_theme'

interface PrefsCtx {
  mode: ThemeMode
  dark: boolean
  locale: Locale
  setMode: (m: ThemeMode) => void
  setLocale: (l: Locale) => void
}

const Ctx = createContext<PrefsCtx | null>(null)

function systemDark(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

export function PrefsProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>(() => {
    const s = localStorage.getItem(THEME_KEY)
    return s === 'light' || s === 'dark' || s === 'auto' ? s : 'auto'
  })
  const [locale, setLocaleState] = useState<Locale>(() => (i18n.language === 'en' ? 'en' : 'zh'))
  const [sysDark, setSysDark] = useState(systemDark)

  // When following the system, listen for system theme changes
  useEffect(() => {
    const mq = window.matchMedia('(prefers-color-scheme: dark)')
    const on = () => setSysDark(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])

  const dark = mode === 'auto' ? sysDark : mode === 'dark'

  const setMode = useCallback((m: ThemeMode) => {
    setModeState(m)
    localStorage.setItem(THEME_KEY, m)
  }, [])

  const setLocale = useCallback((l: Locale) => {
    setLocaleState(l)
    localStorage.setItem(LOCALE_KEY, l)
    i18n.changeLanguage(l)
  }, [])

  // Keep CSS variables and the body background in sync with dark mode (used by markdown borders, etc.)
  useEffect(() => {
    document.documentElement.style.setProperty('--rp-border', dark ? 'rgba(255,255,255,0.16)' : 'rgba(0,0,0,0.12)')
    document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  }, [dark])

  const value = useMemo(() => ({ mode, dark, locale, setMode, setLocale }), [mode, dark, locale, setMode, setLocale])
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function usePrefs(): PrefsCtx {
  const c = useContext(Ctx)
  if (!c) throw new Error('usePrefs must be used within PrefsProvider')
  return c
}
