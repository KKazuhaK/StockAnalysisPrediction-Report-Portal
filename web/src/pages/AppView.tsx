import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Space, Spin, Typography, theme } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useNavigate, useParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { usePrefs } from '../prefs'
import type { AppTokenResp } from '../api/types'
import { API_MESSAGE, API_RESULT, INIT_MESSAGE, THEME_MESSAGE, reqIdOf, validateApiRequest, type ThemePayload } from '../lib/appBridge'

// AppView hosts one installed app inside a sandboxed iframe and mediates its API
// access. The iframe (sandbox="allow-scripts", so a null origin) can reach the
// portal only by posting a message; we validate it against the app's granted
// scopes and, if allowed, perform the /api/v1 call with a short-lived scoped token
// the iframe never sees. See docs/adr/0003-downloadable-apps.md.
export default function AppView() {
  const { id = '' } = useParams()
  const { t } = useTranslation()
  const navigate = useNavigate()
  const { token: tk } = theme.useToken()
  const { dark } = usePrefs()
  const iframeRef = useRef<HTMLIFrameElement>(null)
  const sessionRef = useRef<AppTokenResp | null>(null)
  const [meta, setMeta] = useState<AppTokenResp | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loaded, setLoaded] = useState(false)

  // The theme snapshot handed to the app on load and re-sent on every change, so it
  // can match the host's light/dark and colours without any API access.
  const themePayload = useMemo<ThemePayload>(
    () => ({
      dark,
      colorPrimary: tk.colorPrimary,
      colorBg: tk.colorBgContainer,
      colorText: tk.colorText,
      colorBorder: tk.colorBorderSecondary,
      colorBgLayout: tk.colorBgLayout,
      borderRadius: tk.borderRadius,
    }),
    [dark, tk.colorPrimary, tk.colorBgContainer, tk.colorText, tk.colorBorderSecondary, tk.colorBgLayout, tk.borderRadius],
  )

  // (Re)mint a scoped bridge token for this app. Returns the fresh session or null.
  const mint = useCallback(async (): Promise<AppTokenResp | null> => {
    try {
      const res = await fetch(`/api/apps/${encodeURIComponent(id)}/token`, {
        method: 'POST',
        credentials: 'same-origin',
      })
      if (!res.ok) {
        setError(res.status === 404 ? t('apps.notFound') : t('apps.tokenFailed'))
        return null
      }
      const s: AppTokenResp = await res.json()
      sessionRef.current = s
      setMeta(s)
      return s
    } catch {
      setError(t('apps.tokenFailed'))
      return null
    }
  }, [id, t])

  useEffect(() => {
    mint()
  }, [mint])

  // Perform one validated API call on the iframe's behalf, re-minting once on 401
  // (an expired token), and reply to the iframe with the result.
  const handleApi = useCallback(
    async (data: unknown, replyTo: Window) => {
      const reqId = reqIdOf(data)
      const post = (payload: Record<string, unknown>) =>
        replyTo.postMessage({ type: API_RESULT, reqId, ...payload }, '*')

      const s = sessionRef.current
      if (!s) {
        post({ ok: false, error: 'not ready' })
        return
      }
      const v = validateApiRequest(data, s.scopes)
      if (!v.ok) {
        post({ ok: false, error: v.error })
        return
      }
      const call = (token: string) =>
        fetch(v.path, {
          method: v.method,
          headers: {
            Authorization: `Bearer ${token}`,
            ...(v.method !== 'GET' ? { 'Content-Type': 'application/json' } : {}),
          },
          credentials: 'same-origin',
          body: v.method !== 'GET' && v.body !== undefined ? JSON.stringify(v.body) : undefined,
        })
      try {
        let res = await call(s.token)
        if (res.status === 401) {
          const fresh = await mint()
          if (fresh) res = await call(fresh.token)
        }
        const body = await res.text()
        let json: unknown = body
        try {
          json = body ? JSON.parse(body) : null
        } catch {
          /* leave as text */
        }
        post({ ok: res.ok, status: res.status, data: json })
      } catch (e) {
        post({ ok: false, error: String(e) })
      }
    },
    [mint],
  )

  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      // Only trust messages from THIS app's frame; ignore everything else.
      const frame = iframeRef.current
      if (!frame || e.source !== frame.contentWindow) return
      const data = e.data as { type?: string }
      if (data?.type === API_MESSAGE && e.source) handleApi(e.data, e.source as Window)
    }
    window.addEventListener('message', onMessage)
    return () => window.removeEventListener('message', onMessage)
  }, [handleApi])

  // On load, hand the app the current theme so it can match the host from the start.
  const onLoad = () => {
    setLoaded(true)
    iframeRef.current?.contentWindow?.postMessage({ type: INIT_MESSAGE, theme: themePayload }, '*')
  }

  // Re-post the theme whenever it changes (e.g. the user toggles light/dark while the
  // app is open) so the app can follow live. Only after load, when the frame exists.
  useEffect(() => {
    if (!loaded) return
    iframeRef.current?.contentWindow?.postMessage({ type: THEME_MESSAGE, theme: themePayload }, '*')
  }, [loaded, themePayload])

  if (error) {
    return (
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/apps')}>
          {t('apps.back')}
        </Button>
        <Alert type="error" showIcon message={error} />
      </Space>
    )
  }
  if (!meta) {
    return (
      <div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}>
        <Spin size="large" />
      </div>
    )
  }

  return (
    <Space direction="vertical" size={12} style={{ width: '100%' }}>
      <Space style={{ width: '100%', justifyContent: 'space-between' }} wrap>
        <Space>
          <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/apps')}>
            {t('apps.back')}
          </Button>
          <Typography.Text strong style={{ fontSize: 16 }}>
            {meta.app.icon && <span style={{ marginInlineEnd: 6 }}>{meta.app.icon}</span>}
            {meta.app.name}
          </Typography.Text>
          {meta.app.version && <Typography.Text type="secondary">v{meta.app.version}</Typography.Text>}
        </Space>
      </Space>
      <iframe
        ref={iframeRef}
        title={meta.app.name}
        onLoad={onLoad}
        // allow-scripts WITHOUT allow-same-origin: the app runs with a null origin,
        // so it cannot read the host DOM, the session cookie, or localStorage.
        sandbox="allow-scripts"
        src={`/app-assets/${encodeURIComponent(id)}/${meta.app.entry || 'index.html'}`}
        style={{
          width: '100%',
          height: 'calc(100vh - 220px)',
          minHeight: 420,
          border: `1px solid ${tk.colorBorderSecondary}`,
          borderRadius: tk.borderRadius,
          // Match the theme surface (same token sent to the app as themePayload.colorBg), not a
          // fixed white — otherwise the iframe flashes white on open / bleeds white in dark mode.
          background: tk.colorBgContainer,
        }}
      />
    </Space>
  )
}
