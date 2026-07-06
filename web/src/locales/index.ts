import type { Locale } from 'antd/es/locale'

// A language bundle: the UI strings plus the framework locale packs that must travel with
// them (antd ConfigProvider locale + dayjs locale id). One chunk per language, lazy-loaded.
export interface LangBundle {
  translation: Record<string, string>
  antd: Locale
  dayjs: string // dayjs locale id; the bundle module also side-effect-imports it
}

export interface LangMeta {
  code: string // BCP-47: zh-CN 简体 / zh-TW 繁體 / en-US …
  label: string // native short name shown in the switcher
  load: () => Promise<{ default: LangBundle }>
}

export const BASE_LANG = 'zh-CN'

// Add a language: create locales/<code>.json + locales/bundles/<code>.ts, then one line here.
export const LANGS: LangMeta[] = [
  { code: 'zh-CN', label: '简体中文', load: () => import('./bundles/zh-CN') },
  { code: 'zh-TW', label: '繁體中文', load: () => import('./bundles/zh-TW') },
  { code: 'en-US', label: 'English', load: () => import('./bundles/en-US') },
]

export function findLang(code: string): LangMeta | undefined {
  return LANGS.find((l) => l.code === code)
}

// normalizeSaved maps a stored/legacy locale to a supported code (legacy 'zh' / 'en').
export function normalizeSaved(v: string | null): string {
  if (v === 'zh') return 'zh-CN'
  if (v === 'en') return 'en-US'
  return v && findLang(v) ? v : BASE_LANG
}

// matchLang maps one BCP-47 tag to a supported code (or undefined). Chinese is resolved by
// subtag: an explicit script wins over region — Hans → 简体, Hant → 繁體; otherwise the
// region decides (TW / HK / MO → 繁體; CN / SG / bare zh → 简体).
function matchLang(raw: string): string | undefined {
  const sub = raw.toLowerCase().split('-')
  const tag = sub.join('-')
  const exact = LANGS.find((l) => l.code.toLowerCase() === tag)
  if (exact) return exact.code
  if (sub[0] === 'zh') {
    if (sub.includes('hans')) return 'zh-CN'
    if (sub.includes('hant')) return 'zh-TW'
    return sub.some((s) => s === 'tw' || s === 'hk' || s === 'mo') ? 'zh-TW' : 'zh-CN'
  }
  return LANGS.find((l) => l.code.toLowerCase().split('-')[0] === sub[0])?.code
}

// detectLang picks the best supported language from the browser's ordered preferences
// (navigator.languages), falling back to the base language. Pass prefs explicitly to test.
export function detectLang(prefs?: readonly string[]): string {
  const list =
    prefs ?? (typeof navigator !== 'undefined' ? (navigator.languages ?? [navigator.language]) : [])
  for (const raw of list) {
    const m = raw && matchLang(raw)
    if (m) return m
  }
  return BASE_LANG
}
