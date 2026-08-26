import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { AuthProvider, useAuth } from './auth'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

// The wiring under the blank-page bug: the client notices the session is gone, and the provider is
// what has to act on it. Until it does, the app still believes somebody is signed in — so the route
// gate keeps them on a page whose every request fails, with nothing on screen and no way back.

const ok = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { 'content-type': 'application/json' } })
const unauthorized = (code?: string) =>
  new Response(JSON.stringify({ error: 'no', ...(code ? { code } : {}) }), {
    status: 401,
    headers: { 'content-type': 'application/json' },
  })

function Probe() {
  const { user, expired, loading } = useAuth()
  if (loading) return <div>loading</div>
  return <div>{`user=${user ?? 'none'} expired=${expired}`}</div>
}

const show = () => screen.getByText(/^user=/).textContent

afterEach(() => vi.unstubAllGlobals())

describe('AuthProvider and a session that ends underneath it', () => {
  let calls: string[]
  beforeEach(() => {
    calls = []
  })

  const mount = (afterMe: () => Response) => {
    vi.stubGlobal('fetch', (input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(url)
      if (url === '/api/me' && calls.filter((c) => c === '/api/me').length === 1) {
        return Promise.resolve(ok({ user: 'alice', admin: false, perms: {} }))
      }
      return Promise.resolve(afterMe())
    })
    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )
  }

  it('signs the user out when a request reports the session gone', async () => {
    mount(() => unauthorized('session_expired'))
    await waitFor(() => expect(show()).toBe('user=alice expired=false'))

    // Any later call — this is the one the page would have made — carries the news.
    const { api } = await import('./api/client')
    await api.get('/api/home').catch(() => {})

    await waitFor(() => expect(show()).toBe('user=none expired=true'))
  })

  it('leaves the session alone for a 401 that is not about the session', async () => {
    mount(() => unauthorized('bad_credentials'))
    await waitFor(() => expect(show()).toBe('user=alice expired=false'))

    const { api } = await import('./api/client')
    await api.post('/api/me/password', {}).catch(() => {})

    // Still signed in: a refused password is not a lost session, and throwing the user out here
    // would end a session they still have, mid-task.
    expect(show()).toBe('user=alice expired=false')
  })

  it('does not claim a session expired for somebody who never had one', async () => {
    vi.stubGlobal('fetch', () => Promise.resolve(unauthorized('session_expired')))
    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>,
    )
    await waitFor(() => expect(show()).toBe('user=none expired=false'))
  })
})
