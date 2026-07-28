import { useEffect, useState } from 'react'
import { Button, Card, Form, Input, Modal, Segmented, Select, Space, Typography, theme } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { Navigate, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../auth'
import type { SSOProviderInfo } from '../api/types'
import { usePrefs } from '../prefs'
import { api, ApiError } from '../api/client'
import { SiteLogo, useSite } from '../site'
import { AutoIcon, MoonIcon, SunIcon } from '../components/icons'

export default function LoginPage() {
  const { t } = useTranslation()
  const { title } = useSite()
  const { user, loading, login, loginTOTP } = useAuth()
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

  useEffect(() => {
    // Empty (or failing) means SSO is off, and the login page simply shows nothing extra.
    api
      .get<{ providers: SSOProviderInfo[] }>('/api/sso/providers')
      .then((r) => setProviders(r.providers || []))
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

  const onFinish = async (v: { username: string; password: string }) => {
    setErr('')
    setBusy(true)
    try {
      const { totpToken: pending } = await login(v.username, v.password)
      if (pending) {
        setTotpToken(pending)
        return
      }
      navigate('/')
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : t('login.error'))
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
                <Button type="link" size="small" block onClick={cancelTOTP}>
                  {t('common.cancel')}
                </Button>
              </Form>
            ) : (
            <Form layout="vertical" onFinish={onFinish} requiredMark={false}>
              <Form.Item name="username" label={t('login.username')} rules={[{ required: true }]}>
                <Input size="large" prefix={<UserOutlined />} autoFocus autoComplete="username" />
              </Form.Item>
              <Form.Item name="password" label={t('login.password')} rules={[{ required: true }]}>
                <Input.Password size="large" prefix={<LockOutlined />} autoComplete="current-password" />
              </Form.Item>
              {err && (
                <Typography.Text type="danger" style={{ display: 'block', marginBottom: 12 }}>
                  {err}
                </Typography.Text>
              )}
              <Button type="primary" size="large" htmlType="submit" block loading={busy}>
                {t('login.submit')}
              </Button>
            </Form>
            )}
            {!totpToken && providers.length > 0 && (
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
                      window.location.href = `/api/auth/${p.kind}/${encodeURIComponent(p.slug)}/start`
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
