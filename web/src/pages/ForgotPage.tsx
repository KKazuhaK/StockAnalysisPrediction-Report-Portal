import { useState } from 'react'
import { Button, Card, Form, Input, Result, Space, Typography, theme } from 'antd'
import { UserOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errText } from '../api/client'
import CaptchaField, { type CaptchaValue } from '../components/CaptchaField'
import { SiteLogo, useSite } from '../site'

// Requesting a password-reset link.
//
// A page, not a dialog, and for the same reason registration is one: it is a form with a captcha,
// a failure state and a result state, and a modal has nowhere to put those without covering the
// thing behind it. It is also linkable — an admin can send someone straight here.

export default function ForgotPage() {
  const { t } = useTranslation()
  const site = useSite()
  const navigate = useNavigate()
  const { token } = theme.useToken()
  const [account, setAccount] = useState('')
  const [captcha, setCaptcha] = useState<CaptchaValue>({})
  const [captchaRound, setCaptchaRound] = useState(0)
  const [busy, setBusy] = useState(false)
  const [sent, setSent] = useState(false)
  const [err, setErr] = useState('')

  const submit = async () => {
    setBusy(true)
    setErr('')
    try {
      await api.post('/api/password/forgot', { account: account.trim(), ...captcha })
      setSent(true)
    } catch (e) {
      // "Always report sent" exists so nobody can enumerate accounts, and it holds for every
      // failure that depends on WHICH account was named. A rejected captcha does not — the server
      // decides it before it looks anything up — so saying so leaks nothing, and saying "sent"
      // instead loses the mail the person is waiting for.
      if (e instanceof ApiError && e.code === 'captcha_failed') {
        setErr(errText(e, t))
        setCaptchaRound((n) => n + 1) // a challenge is consumed on use
      } else {
        setSent(true)
      }
    } finally {
      setBusy(false)
    }
  }

  const shell = (children: React.ReactNode) => (
    <div
      style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 24, background: token.colorBgLayout }}
    >
      <Card style={{ width: 420, maxWidth: '100%' }}>
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          <Space>
            <SiteLogo />
            <Typography.Title level={4} style={{ margin: 0 }}>
              {t('login.forgotTitle')}
            </Typography.Title>
          </Space>
          {children}
        </Space>
      </Card>
    </div>
  )

  if (sent) {
    return shell(
      <Result
        status="success"
        subTitle={t('login.forgotSent')}
        extra={
          <Button type="primary" onClick={() => navigate('/login')}>
            {t('register.backToLogin')}
          </Button>
        }
      />,
    )
  }

  return shell(
    <Form layout="vertical" onFinish={submit} requiredMark={false}>
      <Typography.Text type="secondary">{t('login.forgotHint')}</Typography.Text>
      <Form.Item style={{ marginTop: 12, marginBottom: 12 }}>
        <Input
          size="large"
          autoFocus
          prefix={<UserOutlined />}
          placeholder={t('login.forgotAccount')}
          value={account}
          onChange={(e) => setAccount(e.target.value)}
        />
      </Form.Item>
      {/* The form whose abuse is an outbound-mail flood at somebody else's inbox — the one the
          captcha toggle names. It went years without one on screen. */}
      <CaptchaField context="forgot" account={account} refresh={captchaRound} value={captcha} onChange={setCaptcha} />
      {err && (
        <Typography.Text type="danger" style={{ display: 'block', marginBottom: 12 }}>
          {err}
        </Typography.Text>
      )}
      <Button type="primary" size="large" htmlType="submit" block loading={busy}>
        {t('login.forgotSend')}
      </Button>
      {/* Set off from the primary action rather than tucked under its edge: a link touching the
          button it undoes is a misclick waiting to happen, and on the reset form the misclick
          throws away a captcha the person has just typed. */}
      <Button type="link" size="small" block style={{ marginTop: 12 }} onClick={() => navigate('/login')}>
        {t('register.backToLogin')}
      </Button>
    </Form>,
  )
}
