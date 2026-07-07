import { describe, it, expect, vi, afterEach } from 'vitest'
import { shortcutOfUrl, shortcutUrl, triggerShortcut, RUN_ANALYSIS_EVENT } from './shortcuts'

describe('shortcutOfUrl', () => {
  it('returns undefined for a plain URL or empty', () => {
    expect(shortcutOfUrl('https://example.com')).toBeUndefined()
    expect(shortcutOfUrl('')).toBeUndefined()
    expect(shortcutOfUrl(undefined)).toBeUndefined()
  })

  it('resolves a bare shortcut with no pinned target', () => {
    const r = shortcutOfUrl('rp:run-analysis')
    expect(r?.shortcut.key).toBe('run-analysis')
    expect(r?.param).toBeUndefined()
  })

  it('resolves a shortcut pinned to a numeric target', () => {
    expect(shortcutOfUrl('rp:run-analysis:42')?.param).toBe('42')
    expect(shortcutOfUrl('rp:chat:7')?.shortcut.key).toBe('chat')
    expect(shortcutOfUrl('rp:chat:7')?.param).toBe('7')
  })

  it('keeps a string app id intact, splitting only on the first colon', () => {
    expect(shortcutOfUrl('rp:apps:deep-research')?.param).toBe('deep-research')
    expect(shortcutOfUrl('rp:apps:a:b')?.param).toBe('a:b')
  })

  it('returns undefined for an unknown key', () => {
    expect(shortcutOfUrl('rp:nope')).toBeUndefined()
    expect(shortcutOfUrl('rp:nope:1')).toBeUndefined()
  })
})

describe('shortcutUrl', () => {
  it('builds a bare shortcut when no param', () => {
    expect(shortcutUrl('chat')).toBe('rp:chat')
    expect(shortcutUrl('chat', '')).toBe('rp:chat')
    expect(shortcutUrl('chat', undefined)).toBe('rp:chat')
  })

  it('appends a pinned target', () => {
    expect(shortcutUrl('run-analysis', 42)).toBe('rp:run-analysis:42')
    expect(shortcutUrl('chat', '7')).toBe('rp:chat:7')
    expect(shortcutUrl('apps', 'deep-research')).toBe('rp:apps:deep-research')
  })

  it('round-trips through shortcutOfUrl', () => {
    const url = shortcutUrl('chat', 9)
    const r = shortcutOfUrl(url)
    expect(r?.shortcut.key).toBe('chat')
    expect(r?.param).toBe('9')
  })
})

describe('triggerShortcut', () => {
  afterEach(() => vi.restoreAllMocks())

  it('navigates a route shortcut without a param', () => {
    const nav = vi.fn()
    triggerShortcut(shortcutOfUrl('rp:chat')!.shortcut, nav)
    expect(nav).toHaveBeenCalledWith('/chat')
  })

  it('deep-links chat to a specific assistant via ?target', () => {
    const nav = vi.fn()
    const r = shortcutOfUrl('rp:chat:7')!
    triggerShortcut(r.shortcut, nav, r.param)
    expect(nav).toHaveBeenCalledWith('/chat?target=7')
  })

  it('deep-links apps to a specific installed app via /apps/x/:id', () => {
    const nav = vi.fn()
    const r = shortcutOfUrl('rp:apps:deep-research')!
    triggerShortcut(r.shortcut, nav, r.param)
    expect(nav).toHaveBeenCalledWith('/apps/x/deep-research')
  })

  it('fires a plain event for a bare run-analysis shortcut', () => {
    const spy = vi.spyOn(window, 'dispatchEvent')
    const r = shortcutOfUrl('rp:run-analysis')!
    triggerShortcut(r.shortcut, vi.fn(), r.param)
    const ev = spy.mock.calls[0][0]
    expect(ev.type).toBe(RUN_ANALYSIS_EVENT)
    expect((ev as CustomEvent).detail).toBeUndefined()
  })

  it('carries the pinned target id in the run-analysis event detail', () => {
    const spy = vi.spyOn(window, 'dispatchEvent')
    const r = shortcutOfUrl('rp:run-analysis:42')!
    triggerShortcut(r.shortcut, vi.fn(), r.param)
    const ev = spy.mock.calls[0][0] as CustomEvent
    expect(ev.type).toBe(RUN_ANALYSIS_EVENT)
    expect(ev.detail).toEqual({ targetId: 42 })
  })
})
