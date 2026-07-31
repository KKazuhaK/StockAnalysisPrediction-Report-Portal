import { useEffect, useState } from 'react'
import { Button, Card, Form, Input, Modal, Segmented, Select, Space, Typography, theme } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { Navigate, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../auth'
import type { SSOProviderInfo } from '../api/types'
import { usePrefs } from '../prefs'
import { api, ApiError } from '../api/client'
import { passkeySupported } from '../lib/webauthn'
import { hardNavigate } from '../lib/hardNavigate'
import CaptchaField, { type CaptchaValue } from '../components/CaptchaField'
import { SiteLogo, useSite } from '../site'
import { AutoIcon, MoonIcon, SunIcon } from '../components/icons'

export default function LoginPage() {
  const { t } = useTranslation()
  const { title } = useSite()
  const { user, loading, login, loginTOTP, loginPasskey } = useAuth()
  const { mode, setMode, lang, setLang, langs } = usePrefs()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [forgotOpen, setForgotOpen] = useState(false)
  const [forgotAcct, setForgotAcct] = useState('')
  const [forgotBusy, setForgotBusy] = useState(false)
  const [forgotSent, setForgotSent] = useState(false)
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

        if (r.login_mode === 'sso_redirect' && !bypass && list.length === 1) {
          hardNavigate(`/api/auth/${list[0].kind}/${encodeURIComponent(list[0].slug)}/start`)
        }
      })
      .catch(() => {})
    api
      .get<{ enabled: boolean }>('/api/register/config')
      .then((r) => setCanRegister(!!r.enabled))
      .catch(() => {})
  }, [])

  if (!loading && user) return <Navigate to="/" replace />

  const submitForgot = async () => {
    setForgotBusy(true)
    try {
      await api.post('/api/password/forgot', { account: forgotAcct.trim() })
    } catch {
      // ignore — always report "sent" so accounts can't be enumerated
    } finally {
      setForgotBusy(false)
      setForgotSent(true)
    }
  }
  const closeForgot = () => {
    setForgotOpen(false)
    setForgotSent(false)
    setForgotAcct('')
  }

  const submitTOTP = async () => {
    setErr('')
    setBusy(true)
    try {
      await loginTOTP(totpToken, totpCode.trim())
      navigate('/')
    } catch (e) {
      // The pending token is single-use, so a wrong code means starting over rather than
      // retrying against the same challenge.
      setErr(e instanceof ApiError ? e.message : t('login.error'))
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
      setErr(e instanceof ApiError ? e.message : t('login.error'))
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
      setErr(e instanceof ApiError ? e.message : t('login.error'))
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
              <Button type="primary" size="large" htmlType="submit" block loading={busy}>
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
                <Button type="link" size="small" block onClick={() => setShowLocal(true)}>
                  {t('login.usePassword')}
                </Button>
              )
            )}
            {ssoError && (
              <Typography.Text type="danger" style={{ display: 'block' }}>
                {t('login.ssoFailed', { reason: ssoError })}
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
                    // A full navigation, not fetch: the IdP redirect chain has to happen in the
                    // browser's top-level context.
                    onClick={() => {
                      hardNavigate(`/api/auth/${p.kind}/${encodeURIComponent(p.slug)}/start`)
                    }}
                  >
                    {t('login.ssoWith', { name: p.name })}
                  </Button>
                ))}
              </Space>
            )}
            {!totpToken && (
              <div style={{ textAlign: 'center', marginTop: -8 }}>
                <Button type="link" size="small" onClick={() => setForgotOpen(true)}>
                  {t('login.forgot')}
                </Button>
              </div>
            )}
          </Space>
        </Card>
      </div>

      <Modal
        open={forgotOpen}
        title={t('login.forgotTitle')}
        onCancel={closeForgot}
        onOk={submitForgot}
        confirmLoading={forgotBusy}
        okText={t('login.forgotSend')}
        cancelText={t('common.cancel')}
        footer={forgotSent ? null : undefined}
        destroyOnHidden
      >
        {forgotSent ? (
          <Typography.Paragraph>{t('login.forgotSent')}</Typography.Paragraph>
        ) : (
          <Space direction="vertical" style={{ width: '100%' }}>
            <Typography.Text type="secondary">{t('login.forgotHint')}</Typography.Text>
            <Input
              prefix={<UserOutlined />}
              placeholder={t('login.forgotAccount')}
              value={forgotAcct}
              onChange={(e) => setForgotAcct(e.target.value)}
              onPressEnter={submitForgot}
            />
          </Space>
        )}
      </Modal>
    </div>
  )
}
