import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { api, onSessionLost } from './client'

// A session can end while the page is open, and every request after that is a 401. The app has to
// hear about it in one place — otherwise it keeps its stale idea of who is signed in while
// everything underneath fails, which is a page that renders nothing and explains nothing.
//
// The distinction these tests protect is which 401s mean that. A wrong password at the login form
// and a failed step-up are 401s too, and treating either as "your session is gone" would throw
// somebody out of a session they still have, mid-task.

const reply = (status: number, body: unknown) =>
  vi.fn(() =>
    Promise.resolve(
      new Response(JSON.stringify(body), { status, headers: { 'content-type': 'application/json' } }),
    ),
  )

let lost: ReturnType<typeof vi.fn<() => void>>

beforeEach(() => {
  lost = vi.fn<() => void>()
  onSessionLost(lost)
})
afterEach(() => {
  onSessionLost(null)
  vi.unstubAllGlobals()
})

describe('the session-gone signal', () => {
  it('fires when the server says the session expired', async () => {
    vi.stubGlobal('fetch', reply(401, { error: 'gone', code: 'session_expired' }))
    await expect(api.get('/api/home')).rejects.toThrow()
    expect(lost).toHaveBeenCalledTimes(1)
  })

  it('does not fire for a wrong password', async () => {
    vi.stubGlobal('fetch', reply(401, { error: 'nope', code: 'bad_credentials' }))
    await expect(api.post('/api/login', { username: 'a', password: 'b' })).rejects.toThrow()
    expect(lost).not.toHaveBeenCalled()
  })

  it('does not fire for a 401 that names no reason', async () => {
    vi.stubGlobal('fetch', reply(401, { error: 'unauthorized' }))
    await expect(api.get('/api/symbols')).rejects.toThrow()
    expect(lost).not.toHaveBeenCalled()
  })

  it('does not fire for a permission refusal, which leaves the session intact', async () => {
    vi.stubGlobal('fetch', reply(403, { error: 'forbidden', code: 'session_expired' }))
    await expect(api.get('/api/admin/security')).rejects.toThrow()
    expect(lost).not.toHaveBeenCalled()
  })

  it('fires for an upload too — the whole client, not one code path', async () => {
    vi.stubGlobal('fetch', reply(401, { error: 'gone', code: 'session_expired' }))
    await expect(api.upload('/api/run/upload', new FormData())).rejects.toThrow()
    expect(lost).toHaveBeenCalledTimes(1)
  })

  it('still throws, so the caller that wanted the data is not left waiting', async () => {
    vi.stubGlobal('fetch', reply(401, { error: 'gone', code: 'session_expired' }))
    await expect(api.get('/api/home')).rejects.toMatchObject({ status: 401, code: 'session_expired' })
  })

  it('leaves nothing behind once nobody is subscribed', async () => {
    onSessionLost(null)
    vi.stubGlobal('fetch', reply(401, { error: 'gone', code: 'session_expired' }))
    await expect(api.get('/api/home')).rejects.toThrow()
    expect(lost).not.toHaveBeenCalled()
  })
})
