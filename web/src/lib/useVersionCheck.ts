import { useEffect, useState } from 'react'
import { api } from '../api/client'

export type BuildInfo = { version: string; commit: string; buildDate: string }

// A build is identified by all three fields together: a tag can be re-pushed and a commit
// can repeat across rebuilds, but the trio is unique per binary. In local dev the endpoint
// returns a constant "dev / none / unknown", so this key never changes and the prompt never
// shows — only real CI builds (ldflags-injected identity) differ from one another.
export function buildKey(b: BuildInfo): string {
  return `${b.version}@${b.commit}@${b.buildDate}`
}

// Cloudflare-style "new version available" detector. Polls the (session-gated) /api/version
// endpoint and returns true once the server reports a build different from the one this SPA
// booted against — i.e. a new binary was deployed under a still-open tab. We only surface the
// signal; the caller decides how to prompt and the user chooses when to reload. It also checks
// whenever the tab becomes visible again (a backgrounded tab is the common stale case). Fetch
// errors (offline blip, transient 401 before login) are ignored so a hiccup never fabricates a
// prompt, and polling stops for good once an update is detected.
export function useVersionCheck(pollMs = 5 * 60_000): boolean {
  const [stale, setStale] = useState(false)

  useEffect(() => {
    let boot: string | null = null
    let stopped = false
    let timer: number | undefined

    const check = async () => {
      if (stopped) return
      window.clearTimeout(timer) // coalesce: a visibility-triggered check cancels the pending poll
      // .catch here (not a wrapping try/catch) attaches the rejection handler synchronously at
      // the call site, so an offline/401 blip is swallowed to null rather than surfacing as an
      // unhandled rejection — it just means "no answer this tick, try again".
      const info = await api.get<BuildInfo>('/api/version').catch(() => null)
      if (stopped) return
      if (info) {
        const key = buildKey(info)
        if (boot === null) {
          boot = key
        } else if (key !== boot) {
          stopped = true
          setStale(true)
          return
        }
      }
      timer = window.setTimeout(check, pollMs)
    }

    const onVisible = () => {
      if (document.visibilityState === 'visible') check()
    }

    check()
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      stopped = true
      window.clearTimeout(timer)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [pollMs])

  return stale
}
