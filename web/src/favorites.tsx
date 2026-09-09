import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { ApiError, api } from './api/client'
import type { FavoriteItem, FavoritesResp, QuoteResp } from './api/types'

type Market = QuoteResp['market']

interface FavoritesValue {
  items: FavoriteItem[]
  limit: number
  loaded: boolean
  loading: boolean
  error: unknown
  reordering: boolean
  ensureLoaded: (force?: boolean) => Promise<void>
  isFavorite: (market: Market, symbol: string) => boolean
  isBusy: (market: Market, symbol: string) => boolean
  toggle: (market: Market, symbol: string) => Promise<void>
  reorder: (items: FavoriteItem[]) => Promise<void>
}

const FavoritesContext = createContext<FavoritesValue | null>(null)

function favoriteKey(market: string, symbol: string) {
  return `${market}${symbol}`
}

function normalizeItems(items: FavoriteItem[]): FavoriteItem[] {
  return [...items]
    .sort((a, b) => a.ord - b.ord)
    .map((item, ord) => ({ ...item, key: item.key || favoriteKey(item.market, item.symbol), ord }))
}

export function FavoritesProvider({ user, children }: { user: string; children: ReactNode }) {
  const [items, setItems] = useState<FavoriteItem[]>([])
  const [limit, setLimit] = useState(500)
  const [loaded, setLoaded] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [busyKeys, setBusyKeys] = useState<Set<string>>(new Set())
  const [reordering, setReordering] = useState(false)
  const itemsRef = useRef(items)
  const loadedRef = useRef(loaded)
  const errorRef = useRef(error)
  const requestRef = useRef<Promise<void> | null>(null)
  const busyRef = useRef(new Set<string>())
  const reorderingRef = useRef(false)

  const replaceItems = useCallback((next: FavoriteItem[]) => {
    const normalized = normalizeItems(next)
    itemsRef.current = normalized
    setItems(normalized)
  }, [])

  const replaceError = useCallback((next: unknown) => {
    errorRef.current = next
    setError(next)
  }, [])

  const ensureLoaded = useCallback(
    (force = false) => {
      if (!force && loadedRef.current) return Promise.resolve()
      if (requestRef.current) return requestRef.current
      setLoading(true)
      const request = api
        .get<FavoritesResp>('/api/favorites')
        .then((response) => {
          replaceItems(response.items || [])
          setLimit(response.limit || 500)
          replaceError(null)
        })
        .catch((cause) => {
          replaceError(cause)
          throw cause
        })
        .finally(() => {
          loadedRef.current = true
          setLoaded(true)
          setLoading(false)
          requestRef.current = null
        })
      requestRef.current = request
      return request
    },
    [replaceError, replaceItems, user],
  )

  const markBusy = useCallback((key: string, busy: boolean) => {
    const next = new Set(busyRef.current)
    if (busy) next.add(key)
    else next.delete(key)
    busyRef.current = next
    setBusyKeys(next)
  }, [])

  const toggle = useCallback(
    async (market: Market, symbol: string) => {
      if (!loadedRef.current) await ensureLoaded()
      if (errorRef.current) throw errorRef.current
      const key = favoriteKey(market, symbol)
      if (busyRef.current.has(key) || reorderingRef.current) return
      const before = itemsRef.current
      const at = before.findIndex((item) => item.market === market && item.symbol === symbol)
      const removing = at >= 0
      if (!removing && before.length >= limit) {
        throw new ApiError(409, 'favorite limit reached', 'favorite_limit')
      }

      markBusy(key, true)
      if (removing) {
        replaceItems(before.filter((_, index) => index !== at))
      } else {
        replaceItems([...before, { market, symbol, key, ord: before.length }])
      }

      try {
        if (removing) {
          await api.del(`/api/favorites/${encodeURIComponent(market)}/${encodeURIComponent(symbol)}`)
        } else {
          const saved = await api.put<FavoriteItem>(
            `/api/favorites/${encodeURIComponent(market)}/${encodeURIComponent(symbol)}`,
          )
          replaceItems(itemsRef.current.map((item) => (item.key === key ? { ...item, ...saved, key } : item)))
        }
      } catch (cause) {
        const current = itemsRef.current
        if (removing) {
          if (!current.some((item) => item.key === key)) {
            const restored = [...current]
            restored.splice(Math.min(at, restored.length), 0, before[at])
            replaceItems(restored)
          }
        } else {
          replaceItems(current.filter((item) => item.key !== key))
        }
        throw cause
      } finally {
        markBusy(key, false)
      }
    },
    [ensureLoaded, limit, markBusy, replaceItems],
  )

  const reorder = useCallback(
    async (next: FavoriteItem[]) => {
      if (!loadedRef.current) await ensureLoaded()
      if (errorRef.current) throw errorRef.current
      if (busyRef.current.size > 0 || reorderingRef.current) return
      const before = itemsRef.current
      const optimistic = normalizeItems(next)
      reorderingRef.current = true
      setReordering(true)
      replaceItems(optimistic)
      try {
        const response = await api.put<FavoritesResp>('/api/favorites/order', {
          items: optimistic.map(({ market, symbol }) => ({ market, symbol })),
        })
        replaceItems(response.items || optimistic)
        setLimit(response.limit || limit)
      } catch (cause) {
        replaceItems(before)
        throw cause
      } finally {
        reorderingRef.current = false
        setReordering(false)
      }
    },
    [ensureLoaded, limit, replaceItems],
  )

  const value = useMemo<FavoritesValue>(
    () => ({
      items,
      limit,
      loaded,
      loading,
      error,
      reordering,
      ensureLoaded,
      isFavorite: (market, symbol) => items.some((item) => item.market === market && item.symbol === symbol),
      isBusy: (market, symbol) => busyKeys.has(favoriteKey(market, symbol)),
      toggle,
      reorder,
    }),
    [busyKeys, ensureLoaded, error, items, limit, loaded, loading, reorder, reordering, toggle],
  )

  return <FavoritesContext.Provider value={value}>{children}</FavoritesContext.Provider>
}

export function useFavorites() {
  const value = useContext(FavoritesContext)
  if (!value) throw new Error('useFavorites must be used inside FavoritesProvider')
  useEffect(() => {
    void value.ensureLoaded().catch(() => {})
  }, [value.ensureLoaded])
  return value
}
