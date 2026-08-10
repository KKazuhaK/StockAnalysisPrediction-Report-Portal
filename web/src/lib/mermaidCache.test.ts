import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { cacheMermaidSVG, flushMermaidSVGCache, resetMermaidUploadMemo } from './mermaidCache'

const okResponse = { ok: true, status: 200 } as Response

describe('cacheMermaidSVG', () => {
  beforeEach(() => {
    resetMermaidUploadMemo()
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(okResponse)))
  })
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('uploads a chart once, however many times it is rendered', async () => {
    // Reading a report, navigating away and coming back remounts every chart, and the SVG is
    // byte-identical each time — the server would be handed the same payload again.
    await cacheMermaidSVG('graph TD; A-->B', '<svg/>', 'dark')
    await cacheMermaidSVG('graph TD; A-->B', '<svg/>', 'dark')
    await flushMermaidSVGCache()
    expect(fetch).toHaveBeenCalledTimes(1)
  })

  it('treats each theme as its own chart', async () => {
    await cacheMermaidSVG('graph TD; A-->B', '<svg/>', 'dark')
    await cacheMermaidSVG('graph TD; A-->B', '<svg light/>', 'light')
    expect(fetch).toHaveBeenCalledTimes(2)
  })

  it('retries after a failed upload rather than remembering it as done', async () => {
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce({ ok: false, status: 500 } as Response)
        .mockResolvedValue(okResponse),
    )
    await cacheMermaidSVG('graph TD; A-->B', '<svg/>', 'dark')
    await cacheMermaidSVG('graph TD; A-->B', '<svg/>', 'dark')
    expect(fetch).toHaveBeenCalledTimes(2)
  })
})
