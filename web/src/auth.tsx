import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from './api/client'
import type { Me } from './api/types'
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
  perms: Record<string, boolean>
  can: (perm: string) => boolean
  loading: boolean
  // Resolves to a pending token when the account has 2FA on: the password alone issues no
  // session, and the caller must then complete loginTOTP (ADR 0023).
  login: (username: string, password: string) => Promise<{ totpToken?: string }>
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
      perms: me?.perms ?? {},
      can: (perm: string) => (me?.admin ?? false) || !!me?.perms?.[perm],
      loading,
      login: async (username, password) => {
        const res = await api.post<Me & { totp_required?: boolean; token?: string }>('/api/login', {
          username,
          password,
        })
        if (res.totp_required && res.token) return { totpToken: res.token }
        setMe(res as Me)
        return {}
      },
      loginTOTP: async (token, code) => {
        const res = await api.post<Me>('/api/login/2fa', { token, code })
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
        setMe(res)
      },
      logout: async () => {
        await api.post('/api/logout')
        setMe(null)
      },
    }),
    [me, loading],
  )

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useAuth(): AuthCtx {
  const c = useContext(Ctx)
  if (!c) throw new Error('useAuth must be used within AuthProvider')
  return c
}
