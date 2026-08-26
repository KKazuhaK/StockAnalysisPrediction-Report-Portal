import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, ApiError, onSessionLost } from './api/client'
import type { Me } from './api/types'
import type { CaptchaValue } from './components/CaptchaField'
import { getCredential } from './lib/webauthn'

interface AuthCtx {
  user: string | null
  name: string | null // display name, falls back to username
  admin: boolean
  email: string | null // the user's email, or null
  mailEnabled: boolean // SMTP configured → email opt-ins can be offered
  federated: boolean // credentials are the IdP's: local password / 2FA controls do not apply
  totpEnabled: boolean
  passkeyCount: number
  refresh: () => Promise<void> // re-read /api/me after a credential change
  // The session ended while the page was open, rather than never having existed. The login form
  // says so; without it, being thrown back to a blank login page reads as the app losing its place.
  expired: boolean
  perms: Record<string, boolean>
  can: (perm: string) => boolean
  loading: boolean
  // Resolves to a pending token when the account has 2FA on: the password alone issues no
  // session, and the caller must then complete loginTOTP (ADR 0023).
  // captcha carries the public-form proof when the server is asking for one (ADR: captcha gate).
  login: (username: string, password: string, captcha?: CaptchaValue) => Promise<{ totpToken?: string }>
  loginTOTP: (token: string, code: string) => Promise<void>
  // A passkey is the second factor of that same password leg, not a way past it: it consumes the
  // pending token, exactly like a code does.
  loginPasskey: (token: string) => Promise<void>
  logout: () => Promise<void>
}

const Ctx = createContext<AuthCtx | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [me, setMe] = useState<Me | null>(null)
  const [loading, setLoading] = useState(true)
  const [expired, setExpired] = useState(false)
  // Whether there was a session to lose, read inside a callback the client owns. "Expired" is a
  // claim about the past: someone who never signed in gets the ordinary login form, not a notice
  // about a session they never had.
  const signedIn = useRef(false)
  signedIn.current = me != null

  // One subscriber for the whole app: any request that comes back "the session is gone" drops the
  // user here, and the route gate takes it from there. Without this the app keeps its stale idea of
  // who is signed in while every call underneath it fails — which is the blank page with no message.
  useEffect(() => {
    onSessionLost(() => {
      if (signedIn.current) setExpired(true)
      setMe(null)
    })
    return () => onSessionLost(null)
  }, [])

  useEffect(() => {
    api
      .get<Me>('/api/me')
      .then(setMe)
      .catch((e) => {
        if (!(e instanceof ApiError && e.status === 401)) console.error(e)
        setMe(null)
      })
      .finally(() => setLoading(false))
  }, [])

  const value = useMemo<AuthCtx>(
    () => ({
      user: me?.user ?? null,
      name: me?.name ?? me?.user ?? null,
      admin: me?.admin ?? false,
      email: me?.email || null,
      mailEnabled: me?.mail_enabled ?? false,
      federated: me?.federated ?? false,
      totpEnabled: me?.totp_enabled ?? false,
      passkeyCount: me?.passkeys ?? 0,
      refresh: async () => {
        try {
          setMe(await api.get<Me>('/api/me'))
        } catch (e) {
          if (!(e instanceof ApiError && e.status === 401)) console.error(e)
        }
      },
      expired,
      perms: me?.perms ?? {},
      can: (perm: string) => (me?.admin ?? false) || !!me?.perms?.[perm],
      loading,
      login: async (username, password, captcha) => {
        const res = await api.post<Me & { totp_required?: boolean; token?: string }>('/api/login', {
          username,
          password,
          ...(captcha ?? {}),
        })
        if (res.totp_required && res.token) return { totpToken: res.token }
        setExpired(false)
        setMe(res as Me)
        return {}
      },
      loginTOTP: async (token, code) => {
        const res = await api.post<Me>('/api/login/2fa', { token, code })
        setExpired(false)
        setMe(res)
      },
      loginPasskey: async (token) => {
        const begin = await api.post<{ token: string; pending: string; options: { publicKey?: unknown } }>(
          '/api/login/passkey/begin',
          { token },
        )
        const opts = (begin.options as { publicKey?: unknown }).publicKey ?? begin.options
        const assertion = await getCredential(opts)
        const res = await api.post<Me>(
          `/api/login/passkey/finish?token=${encodeURIComponent(begin.token)}&pending=${encodeURIComponent(begin.pending)}`,
          assertion,
        )
        setExpired(false)
        setMe(res)
      },
      logout: async () => {
        await api.post('/api/logout')
        // Signing out is not expiring: the notice would be telling someone their session ended
        // when what happened is that they ended it.
        setExpired(false)
        setMe(null)
      },
    }),
    [me, loading, expired],
  )

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useAuth(): AuthCtx {
  const c = useContext(Ctx)
  if (!c) throw new Error('useAuth must be used within AuthProvider')
  return c
}
