import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import dayjs from 'dayjs'
import type { Locale as AntdLocale } from 'antd/es/locale'
import i18n, { LOCALE_KEY } from './i18n'
import base from './locales/bundles/zh-CN'
import { BASE_LANG, LANGS, findLang, normalizeSaved, detectLang, type LangBundle } from './locales'

export type ThemeMode = 'light' | 'dark' | 'auto'

const THEME_KEY = 'rp_theme'

export interface LangOption {
  code: string
  label: string
}

interface PrefsCtx {
  mode: ThemeMode
  dark: boolean
  lang: string
  antd: AntdLocale // antd ConfigProvider locale for the active language
  langs: LangOption[]
  setMode: (m: ThemeMode) => void
  setLang: (code: string) => void
}

const Ctx = createContext<PrefsCtx | null>(null)
const LANG_OPTIONS: LangOption[] = LANGS.map((l) => ({ code: l.code, label: l.label }))

function systemDark(): boolean {
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

export function PrefsProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>(() => {
    const s = localStorage.getItem(THEME_KEY)
    return s === 'light' || s === 'dark' || s === 'auto' ? s : 'auto'
  })
  // Explicit saved choice wins; first visit (nothing saved) follows the browser language.
  const [lang, setLangState] = useState<string>(() => {
    const saved = localStorage.getItem(LOCALE_KEY)
    return saved ? normalizeSaved(saved) : detectLang()
  })
  const [antd, setAntd] = useState<AntdLocale>(base.antd)
  const [sysDark, setSysDark] = useState(systemDark)
  const reqRef = useRef(0) // sequence token: only the latest language request may apply (guards out-of-order lazy loads)

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

  // apply activates a loaded bundle: register its strings once, then switch i18n + dayjs + antd.
  const apply = useCallback((code: string, b: LangBundle) => {
    if (!i18n.hasResourceBundle(code, 'translation')) {
      i18n.addResourceBundle(code, 'translation', b.translation)
    }
    dayjs.locale(b.dayjs)
    setAntd(b.antd)
    void i18n.changeLanguage(code)
  }, [])

  const setLang = useCallback(
    (code: string) => {
      const meta = findLang(code)
      if (!meta) return
      const req = (reqRef.current += 1)
      // Apply (and only then persist / update state) after the bundle loads, and only if this
      // is still the latest request — so out-of-order lazy loads can't leave the UI on a
      // language the switcher doesn't show, and a failed load never desyncs or persists.
      meta.load().then(
        (m) => {
          if (req !== reqRef.current) return
          apply(code, m.default)
          setLangState(code)
          localStorage.setItem(LOCALE_KEY, code)
        },
        () => {
          /* bundle failed to load (e.g. a stale chunk after redeploy): keep the current language */
        },
      )
    },
    [apply],
  )

  // On mount, activate the saved language. The base language's strings + antd are already
  // bundled (only dayjs needs setting); other languages are lazy-loaded and swapped in.
  useEffect(() => {
    // Self-heal a stored legacy code (e.g. 'zh' → 'zh-CN'), but leave a first-visit detection
    // unpersisted so it keeps following the browser until the user explicitly picks a language.
    const saved = localStorage.getItem(LOCALE_KEY)
    if (saved && saved !== lang) localStorage.setItem(LOCALE_KEY, lang)
    if (lang === BASE_LANG) {
      dayjs.locale(base.dayjs)
      return
    }
    const req = (reqRef.current += 1)
    findLang(lang)?.load().then(
      (m) => {
        if (req === reqRef.current) apply(lang, m.default)
      },
      () => {
        // saved/detected chunk unreachable → fall back to the bundled base so the switcher and
        // the rendered UI stay consistent (base strings/antd are already active).
        if (req === reqRef.current) setLangState(BASE_LANG)
      },
    )
    // run once on mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Keep CSS variables and the body background in sync with dark mode (used by markdown borders, etc.)
  useEffect(() => {
    document.documentElement.style.setProperty('--rp-border', dark ? 'rgba(255,255,255,0.16)' : 'rgba(0,0,0,0.12)')
    document.documentElement.dataset.theme = dark ? 'dark' : 'light'
  }, [dark])

  const value = useMemo(
    () => ({ mode, dark, lang, antd, langs: LANG_OPTIONS, setMode, setLang }),
    [mode, dark, lang, antd, setMode, setLang],
  )
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function usePrefs(): PrefsCtx {
  const c = useContext(Ctx)
  if (!c) throw new Error('usePrefs must be used within PrefsProvider')
  return c
}
