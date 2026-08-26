import { useEffect, useState } from 'react'
import { Alert, Button, Card, Form, Input, Modal, Segmented, Select, Space, Typography, theme } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { Navigate, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../auth'
import type { SSOProviderInfo } from '../api/types'
import { usePrefs } from '../prefs'
import { api, ApiError, errText } from '../api/client'
import { passkeySupported } from '../lib/webauthn'
import { hardNavigate } from '../lib/hardNavigate'
import CaptchaField, { type CaptchaValue } from '../components/CaptchaField'
import SSOIcon from '../components/SSOIcon'
import { SiteLogo, useSite } from '../site'
import { AutoIcon, MoonIcon, SunIcon } from '../components/icons'

/**
 * Turns a failure code from ssoFail into a sentence. Falls back to the bare code, which is what the
 * page used to print for every failure — nine different SAML rejections all arrived as
 * "bad_response", so the one thing the reader needed was the one thing it never said.
 *
 * Every code here describes the portal's own trust configuration, never anything about the person
 * signing in, so naming them cannot be used to learn whether an account exists.
 */
function ssoReason(code: string, t: (k: string) => string): string {
  const key = `login.ssoReason.${code}`
  const s = t(key)
  return s === key ? code : s
}

export default function LoginPage() {
  const { t } = useTranslation()
  const { title } = useSite()
  const { user, loading, login, loginTOTP, loginPasskey, expired } = useAuth()
  const { mode, setMode, lang, setLang, langs } = usePrefs()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  // Second-factor step: the password leg hands back a single-use token and issues no session.
  const [totpToken, setTotpToken] = useState('')
  const [totpCode, setTotpCode] = useState('')
  const [providers, setProviders] = useState<SSOProviderInfo[]>([])
  // What the server says this page may offer. Resolved server-side (login_mode.go) so the rules —
  // including "degrade to local when no provider is enabled" — live in exactly one place.
  const [offers, setOffers] = useState<{ mode: string; local: boolean; sso: boolean }>({
    mode: 'dual',
    local: true,
    sso: false,
  })
  // sso_first hides the password form behind one deliberate click; this is that click.
  const [showLocal, setShowLocal] = useState(false)
  // Why the last handshake failed, so a loop that used to be silent now explains itself.
  const [ssoError, setSSOError] = useState('')
  // Where force-SSO should send the browser, once we know nobody is already signed in. The slug
  // travels with the URL so the notice below can name the provider without re-deriving it.
  const [redirectTo, setRedirectTo] = useState<{ url: string; slug: string }>({ url: '', slug: '' })
  // The provider whose handshake we have just handed the browser off to. A full navigation gives
  // no feedback of its own: the click sets window.location and then nothing on this page changes
  // until the browser decides to leave, which on a slow link is long enough to read the button as
  // broken and press it again. This is what says otherwise.
  const [leavingTo, setLeavingTo] = useState('')
  const [captcha, setCaptcha] = useState<CaptchaValue>({})
  const [captchaRound, setCaptchaRound] = useState(0)
  const [account, setAccount] = useState('')
  const [canRegister, setCanRegister] = useState(false)

  useEffect(() => {
    // Empty (or failing) means SSO is off, and the login page simply shows nothing extra.
    api
      .get<{ providers: SSOProviderInfo[]; login_mode?: string; local?: boolean; sso?: boolean }>(
        '/api/sso/providers',
      )
      .then((r) => {
        const list = r.providers || []
        setProviders(list)
        // Force-SSO sends the browser straight to the provider, and two things opt out of that.
        // `?local=1` is the operator's escape. `?sso_error=…` is where ssoFail sends a failed
        // handshake — without treating it as a bypass, sso_redirect plus ANY SSO failure is an
        // unbreakable loop: the page bounces to the IdP, the IdP bounces back, the page bounces
        // again. Browsers break SERVER-side redirect chains, never a JS-driven one.
        const params = new URLSearchParams(window.location.search)
        const bypass = params.has('local') || params.has('sso_error')
        setSSOError(params.get('sso_error') || '')

        // `??`, not `||`: an explicit false from the server means local_only and must be honoured,
        // while an ABSENT field means the response predates these fields — fall back to the old
        // contract, which was simply "there are providers, so offer them".
        setOffers({
          mode: r.login_mode || 'dual',
          // A bypass reveals the password form whatever the mode says. That grants nothing —
          // whether a password is ACCEPTED is the server's call on its own axis (sso_only, where
          // admins are exempt) — and it is the difference between an escape hatch and a page whose
          // only control is the identity provider that just failed.
          local: (r.local ?? true) || bypass,
          sso: r.sso ?? list.length > 0,
        })

        // Not fired from here. Two reasons: this promise can resolve after the component has
        // unmounted — the auth check may already have sent an-already-signed-in visitor to "/", and
        // throwing them at the IdP would yank them straight back off it — and the decision needs
        // the auth state, which this callback does not have. Recorded, and acted on in the effect
        // below once both are known.
        setRedirectTo(
          r.login_mode === 'sso_redirect' && !bypass && list.length === 1
            ? { url: `/api/auth/${list[0].kind}/${encodeURIComponent(list[0].slug)}/start`, slug: list[0].slug }
            : { url: '', slug: '' },
        )
      })
      .catch(() => {})
    api
      .get<{ enabled: boolean }>('/api/register/config')
      .then((r) => setCanRegister(!!r.enabled))
      .catch(() => {})
  }, [])

  // Force-SSO's navigation, gated on the auth state. A visitor who already holds a session — a
  // bookmark, a typed URL, Back after the SSO round-trip (a real history entry, because the
  // callback is a top-level redirect) — is sent to "/" by the check below instead.
  useEffect(() => {
    if (loading || user || !redirectTo.url) return
    // Force-SSO leaves on its own, so it gets the same notice — otherwise the page shows a login
    // card for a beat and then vanishes, with nothing having said why.
    setLeavingTo(redirectTo.slug)
    hardNavigate(redirectTo.url)
  }, [loading, user, redirectTo])

  // Coming Back from the identity provider restores this page from the bfcache with its React
  // state intact — including a spinner on a handshake that is over. pageshow is the one event that
  // fires for that restore (it also fires on a normal load, where clearing is a no-op).
  useEffect(() => {
    const onShow = () => setLeavingTo('')
    window.addEventListener('pageshow', onShow)
    return () => window.removeEventListener('pageshow', onShow)
  }, [])

  if (!loading && user) return <Navigate to="/" replace />


  const submitTOTP = async () => {
    setErr('')
    setBusy(true)
    try {
      await loginTOTP(totpToken, totpCode.trim())
      navigate('/')
    } catch (e) {
      // The pending token is single-use, so a wrong code means starting over rather than
      // retrying against the same challenge.
      setErr(errText(e, t, 'login.error'))
      setTotpToken('')
      setTotpCode('')
    } finally {
      setBusy(false)
    }
  }
  const cancelTOTP = () => {
    setTotpToken('')
    setTotpCode('')
    setErr('')
  }

  // A passkey is the SECOND factor of this same password leg — it consumes the pending token the
  // password issued, exactly as a code does. It is deliberately not offered on the password screen:
  // these credentials are registered with user verification "preferred", so one may be
  // possession-only, and the server refuses a passkey presented on its own.
  const submitPasskey = async () => {
    setErr('')
    setBusy(true)
    try {
      await loginPasskey(totpToken)
      navigate('/')
    } catch (e) {
      // A dismissed browser prompt is a change of mind, not a failure worth an error banner — and
      // it must not clear the pending token, or the user would have to type their password again.
      if (e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError')) return
      setErr(errText(e, t, 'login.error'))
    } finally {
      setBusy(false)
    }
  }

  const onFinish = async (v: { username: string; password: string }) => {
    setErr('')
    setBusy(true)
    try {
      const { totpToken: pending } = await login(v.username, v.password, captcha)
      if (pending) {
        setTotpToken(pending)
        return
      }
      navigate('/')
    } catch (e) {
      setErr(errText(e, t, 'login.error'))
      // A challenge is consumed on use, so any refusal must re-arm the field — and a portal in
      // after-failures mode starts asking for one exactly here, on the attempt that crossed it.
      setCaptchaRound((n) => n + 1)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'grid',
        placeItems: 'center',
        padding: 20,
        background: token.colorBgLayout,
      }}
    >
      <div style={{ width: '100%', maxWidth: 560 }}>
        <Card style={{ width: '100%', maxWidth: 380, margin: '0 auto', boxShadow: token.boxShadowSecondary }}>
          <Space direction="vertical" size={20} style={{ width: '100%' }}>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
              <Typography.Title
                level={4}
                style={{ margin: 0, display: 'flex', alignItems: 'center', gap: 8, whiteSpace: 'nowrap' }}
              >
                <SiteLogo size={22} color={token.colorPrimary} />
                {t('login.titleWithBrand', { title })}
              </Typography.Title>
              <Space style={{ justifyContent: 'flex-end' }} wrap>
                <Segmented
                  size="small"
                  value={mode}
                  onChange={(v) => setMode(v as any)}
                  options={[
                    { value: 'light', icon: <SunIcon /> },
                    { value: 'dark', icon: <MoonIcon /> },
                    { value: 'auto', icon: <AutoIcon /> },
                  ]}
                />
                <Select
                  size="small"
                  value={lang}
                  onChange={setLang}
                  style={{ width: 116 }}
                  options={langs.map((l) => ({ value: l.code, label: l.label }))}
                />
              </Space>
              {/* Landing here because a session ran out is not the same as arriving at the login
                  page, and being dropped without a word reads as the app losing its place. */}
              {expired && <Alert type="info" showIcon message={t('login.expired')} />}
            </div>

            {totpToken ? (
              <Form layout="vertical" onFinish={submitTOTP} requiredMark={false}>
                <Typography.Paragraph type="secondary">{t('login.totpHint')}</Typography.Paragraph>
                <Form.Item label={t('login.totpCode')} required>
                  <Input
                    autoFocus
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    value={totpCode}
                    onChange={(e) => setTotpCode(e.target.value)}
                  />
                </Form.Item>
                {err && (
                  <Typography.Text type="danger" style={{ display: 'block', marginBottom: 12 }}>
                    {err}
                  </Typography.Text>
                )}
                <Button type="primary" size="large" htmlType="submit" block loading={busy}>
                  {t('login.totpVerify')}
                </Button>
                {passkeySupported() && (
                  <Button size="large" block onClick={submitPasskey} loading={busy} style={{ marginTop: 8 }}>
                    {t('login.passkeyUse')}
                  </Button>
                )}
                <Button type="link" size="small" block onClick={cancelTOTP}>
                  {t('common.cancel')}
                </Button>
              </Form>
            ) : offers.local && (offers.mode !== 'sso_first' || showLocal || !offers.sso) ? (
            <Form layout="vertical" onFinish={onFinish} requiredMark={false}>
              <Form.Item name="username" label={t('login.username')} rules={[{ required: true }]}>
                <Input size="large" prefix={<UserOutlined />} autoFocus autoComplete="username"
                  onChange={(e) => setAccount(e.target.value)} />
              </Form.Item>
              <Form.Item name="password" label={t('login.password')} rules={[{ required: true }]}>
                <Input.Password size="large" prefix={<LockOutlined />} autoComplete="current-password" />
              </Form.Item>
              <CaptchaField context="login" account={account} refresh={captchaRound}
                value={captcha} onChange={setCaptcha} />
              {err && (
                <Typography.Text type="danger" style={{ display: 'block', marginBottom: 12 }}>
                  {err}
                </Typography.Text>
              )}
              <Button type="primary" size="large" htmlType="submit" block loading={busy} disabled={!!leavingTo}>
                {t('login.submit')}
              </Button>
              {canRegister && (
                <Button type="link" size="small" block onClick={() => navigate('/register')}>
                  {t('login.register')}
                </Button>
              )}
            </Form>
            ) : (
              // sso_first with the form still collapsed, or sso_redirect (where the auto-redirect is
              // either in flight or was declined via ?local=1).
              !totpToken &&
              offers.local && (
                <Button type="link" size="small" block disabled={!!leavingTo} onClick={() => setShowLocal(true)}>
                  {t('login.usePassword')}
                </Button>
              )
            )}
            {ssoError && (
              <Typography.Text type="danger" style={{ display: 'block' }}>
                {t('login.ssoFailed', { reason: ssoReason(ssoError, t) })}
              </Typography.Text>
            )}
            {!totpToken && offers.sso && providers.length > 0 && (
              <Space direction="vertical" size={8} style={{ width: '100%' }}>
                <Typography.Text type="secondary" style={{ textAlign: 'center', display: 'block' }}>
                  {t('login.ssoDivider')}
                </Typography.Text>
                {providers.map((p) => (
                  <Button
                    key={p.slug}
                    size="large"
                    block
                    // The spinner is on the button that was pressed; the others go quiet rather
                    // than inviting a second handshake over the top of the first.
                    loading={leavingTo === p.slug}
                    disabled={!!leavingTo && leavingTo !== p.slug}
                    // A full navigation, not fetch: the IdP redirect chain has to happen in the
                    // browser's top-level context.
                    onClick={() => {
                      setLeavingTo(p.slug)
                      hardNavigate(`/api/auth/${p.kind}/${encodeURIComponent(p.slug)}/start`)
                    }}
                  >
                    <Space size={8}>
                      <SSOIcon icon={p.icon} />
                      {t('login.ssoWith', { name: p.name })}
                    </Space>
                  </Button>
                ))}
                {leavingTo && (
                  <Typography.Text type="secondary" style={{ textAlign: 'center', display: 'block', fontSize: 12 }}>
                    {t('login.ssoRedirecting', { name: providers.find((p) => p.slug === leavingTo)?.name || '' })}
                  </Typography.Text>
                )}
              </Space>
            )}
            {!totpToken && (
              <div style={{ textAlign: 'center', marginTop: -8 }}>
                <Button type="link" size="small" onClick={() => navigate('/forgot')}>
                  {t('login.forgot')}
                </Button>
              </div>
            )}
          </Space>
        </Card>
      </div>

    </div>
  )
}
