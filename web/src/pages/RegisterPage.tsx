import { useEffect, useState } from 'react'
import { Alert, Button, Card, Form, Input, Result, Space, Typography, theme } from 'antd'
import { LockOutlined, MailOutlined } from '@ant-design/icons'
import { Link, Navigate, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import { useAuth } from '../auth'
import { SiteLogo, useSite } from '../site'
import CaptchaField, { type CaptchaValue } from '../components/CaptchaField'

// Self-service signup. The page does not exist unless an admin enabled it — a portal must not
// acquire a public registration form by being upgraded — so it asks the server first and shows a
// plain "not available" rather than a form nobody can submit.

export default function RegisterPage() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { user, loading } = useAuth()
  const navigate = useNavigate()
  const site = useSite()
  const [config, setConfig] = useState<{ enabled: boolean; requires_verification: boolean } | null>(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<'verify' | 'ready' | null>(null)
  const [captcha, setCaptcha] = useState<CaptchaValue>({})
  const [captchaRound, setCaptchaRound] = useState(0)
  const [email, setEmail] = useState('')

  useEffect(() => {
    api
      .get<{ enabled: boolean; requires_verification: boolean }>('/api/register/config')
      .then(setConfig)
      .catch(() => setConfig({ enabled: false, requires_verification: true }))
  }, [])

  if (!loading && user) return <Navigate to="/" replace />

  const submit = async (v: { email: string; password: string; display_name?: string }) => {
    setErr('')
    setBusy(true)
    try {
      const r = await api.post<{ requires_verification?: boolean }>('/api/register', { ...v, ...captcha })
      setDone(r.requires_verification ? 'verify' : 'ready')
    } catch (e) {
      setErr(errText(e, t, 'register.failed'))
      // A challenge is consumed on use, so any refusal — captcha or not — must re-arm the field or
      // the next attempt fails on a stale one.
      setCaptchaRound((n) => n + 1)
    } finally {
      setBusy(false)
    }
  }

  const shell = (children: React.ReactNode) => (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 24,
      background: token.colorBgLayout }}>
      <Card style={{ width: 420, maxWidth: '100%' }}>
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Space>
            <SiteLogo />
            <Typography.Title level={4} style={{ margin: 0 }}>
              {t('register.title', { name: site.title })}
            </Typography.Title>
          </Space>
          {children}
        </Space>
      </Card>
    </div>
  )

  if (config && !config.enabled) {
    return shell(
      <Result
        status="404"
        subTitle={t('register.disabled')}
        extra={<Link to="/login">{t('register.backToLogin')}</Link>}
      />,
    )
  }
  if (done) {
    return shell(
      <Result
        status="success"
        title={done === 'verify' ? t('register.checkEmailTitle') : t('register.readyTitle')}
        subTitle={done === 'verify' ? t('register.checkEmail', { email }) : t('register.ready')}
        extra={
          done === 'ready' ? (
            <Button type="primary" onClick={() => navigate('/login')}>
              {t('register.backToLogin')}
            </Button>
          ) : undefined
        }
      />,
    )
  }

  return shell(
    <Form layout="vertical" onFinish={submit} requiredMark={false}>
      <Form.Item
        name="email"
        label={t('register.email')}
        rules={[{ required: true }, { type: 'email', message: t('register.emailInvalid') }]}
      >
        <Input size="large" prefix={<MailOutlined />} autoFocus autoComplete="email"
          onChange={(e) => setEmail(e.target.value)} />
      </Form.Item>
      <Form.Item name="display_name" label={t('register.displayName')}>
        <Input size="large" autoComplete="name" />
      </Form.Item>
      <Form.Item
        name="password"
        label={t('register.password')}
        rules={[{ required: true }, { min: 12, message: t('reset.tooShort') }]}
      >
        <Input.Password size="large" prefix={<LockOutlined />} autoComplete="new-password" />
      </Form.Item>
      <CaptchaField context="register" account={email} refresh={captchaRound}
        value={captcha} onChange={setCaptcha} />
      {err && (
        <Typography.Text type="danger" style={{ display: 'block', marginBottom: 12 }}>
          {err}
        </Typography.Text>
      )}
      {config?.requires_verification && (
        <Alert type="info" showIcon message={t('register.willVerify')} style={{ marginBottom: 12 }} />
      )}
      <Button type="primary" size="large" htmlType="submit" block loading={busy}>
        {t('register.submit')}
      </Button>
      <Button type="link" size="small" block onClick={() => navigate('/login')}>
        {t('register.backToLogin')}
      </Button>
    </Form>,
  )
}
