import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { UNCHANGED, forgetTags, getIfChanged } from './conditionalGet'

const respond = (status: number, body: string, etag?: string) =>
  ({
    ok: status < 400,
    status,
    statusText: '',
    text: () => Promise.resolve(body),
    headers: { get: (k: string) => (k === 'ETag' && etag ? etag : null) },
  }) as unknown as Response

describe('getIfChanged', () => {
  beforeEach(() => forgetTags())
  afterEach(() => vi.unstubAllGlobals())

  it('sends back the tag it was given, and reports an unchanged answer as such', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(respond(200, '{"n":1}', 'W/"abc"')).mockResolvedValueOnce(respond(304, ''))
    vi.stubGlobal('fetch', fetchMock)

    expect(await getIfChanged('/api/q')).toEqual({ n: 1 })
    expect(fetchMock.mock.calls[0][1].headers).toBeUndefined() // nothing to revalidate against yet

    expect(await getIfChanged('/api/q')).toBe(UNCHANGED)
    expect(fetchMock.mock.calls[1][1].headers['If-None-Match']).toBe('W/"abc"')
  })

  it('forgets the tag when a request fails, so the next poll asks properly', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(respond(200, '{"n":1}', 'W/"abc"'))
      .mockResolvedValueOnce(respond(500, '{"error":"boom"}'))
      .mockResolvedValueOnce(respond(200, '{"n":2}', 'W/"def"'))
    vi.stubGlobal('fetch', fetchMock)

    await getIfChanged('/api/q')
    await expect(getIfChanged('/api/q')).rejects.toThrow('boom')
    await getIfChanged('/api/q')
    expect(fetchMock.mock.calls[2][1].headers).toBeUndefined()
  })

  it('drops the tag when a response arrives without one, rather than revalidating against a stale key', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(respond(200, '{"n":1}', 'W/"abc"'))
      .mockResolvedValueOnce(respond(200, '{"n":2}'))
      .mockResolvedValueOnce(respond(200, '{"n":3}'))
    vi.stubGlobal('fetch', fetchMock)

    await getIfChanged('/api/q')
    await getIfChanged('/api/q')
    await getIfChanged('/api/q')
    expect(fetchMock.mock.calls[2][1].headers).toBeUndefined()
  })
})
