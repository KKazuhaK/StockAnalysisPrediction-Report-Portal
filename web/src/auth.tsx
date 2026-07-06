import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, ApiError } from './api/client'
import type { Me } from './api/types'

interface AuthCtx {
  user: string | null
  name: string | null // display name, falls back to username
  admin: boolean
  email: string | null // the user's email, or null
  mailEnabled: boolean // SMTP configured → email opt-ins can be offered
  perms: Record<string, boolean>
  can: (perm: string) => boolean
  loading: boolean
  login: (username: string, password: string) => Promise<void>
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
      perms: me?.perms ?? {},
      can: (perm: string) => (me?.admin ?? false) || !!me?.perms?.[perm],
      loading,
      login: async (username, password) => {
        const res = await api.post<Me>('/api/login', { username, password })
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
