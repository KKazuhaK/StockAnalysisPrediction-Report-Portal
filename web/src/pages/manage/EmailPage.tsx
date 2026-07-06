import { useEffect, useState } from 'react'
import { App, Button, Card, Divider, Input, InputNumber, Select, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'

interface EmailConfig {
  enabled: boolean
  host: string
  port: number
  user: string
  from: string
  security: string
  has_pass: boolean
  public_url: string
}

// Admin SMTP config for password reset + run-done notifications. Feature settings
// live in the DB (managed here), never in config.yaml. The password is write-only:
// GET never returns it, and a blank field keeps the saved one.
export default function EmailPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [enabled, setEnabled] = useState(false)
  const [host, setHost] = useState('')
  const [port, setPort] = useState(587)
  const [user, setUser] = useState('')
  const [pass, setPass] = useState('')
  const [hasPass, setHasPass] = useState(false)
  const [from, setFrom] = useState('')
  const [security, setSecurity] = useState('starttls')
  const [publicUrl, setPublicUrl] = useState('')
  const [testTo, setTestTo] = useState('')
  const [saving, setSaving] = useState(false)
  const [testing, setTesting] = useState(false)

  const load = () =>
    api.get<EmailConfig>('/api/admin/email').then((r) => {
      setEnabled(r.enabled)
      setHost(r.host)
      setPort(r.port || 587)
      setUser(r.user)
      setFrom(r.from)
      setSecurity(r.security || 'starttls')
      setPublicUrl(r.public_url || '')
      setHasPass(r.has_pass)
      setPass('')
    })
  useEffect(() => {
    load()
  }, [])

  const save = async () => {
    setSaving(true)
    try {
      await api.post('/api/admin/email', { enabled, host, port, user, pass, from, security, public_url: publicUrl })
      message.success(t('common.saved'))
      load()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSaving(false)
    }
  }
  const sendTest = async () => {
    setTesting(true)
    try {
      await api.post('/api/admin/email/test', { to: testTo })
      message.success(t('email.testSent'))
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setTesting(false)
    }
  }

  const row = (label: string, control: React.ReactNode) => (
    <Space wrap>
      <span style={{ display: 'inline-block', minWidth: 120 }}>{label}</span>
      {control}
    </Space>
  )

  return (
    <Card title={t('nav.email')}>
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text type="secondary">{t('email.hint')}</Typography.Text>
        {row(t('email.enabled'), <Switch checked={enabled} onChange={setEnabled} />)}
        {row(t('email.host'), <Input style={{ width: 260 }} value={host} onChange={(e) => setHost(e.target.value)} placeholder="smtp.example.com" />)}
        {row(t('email.port'), <InputNumber min={1} max={65535} value={port} onChange={(v) => setPort(v || 587)} />)}
        {row(
          t('email.security'),
          <Select
            style={{ width: 180 }}
            value={security}
            onChange={setSecurity}
            options={[
              { value: 'starttls', label: 'STARTTLS (587)' },
              { value: 'tls', label: 'TLS / SSL (465)' },
              { value: 'none', label: t('email.securityNone') },
            ]}
          />,
        )}
        {row(t('email.user'), <Input style={{ width: 260 }} value={user} onChange={(e) => setUser(e.target.value)} autoComplete="off" />)}
        {row(
          t('email.pass'),
          <Input.Password
            style={{ width: 260 }}
            value={pass}
            onChange={(e) => setPass(e.target.value)}
            placeholder={hasPass ? t('email.passKeep') : ''}
            autoComplete="new-password"
          />,
        )}
        {row(t('email.from'), <Input style={{ width: 260 }} value={from} onChange={(e) => setFrom(e.target.value)} placeholder="noreply@example.com" />)}
        {row(t('email.publicUrl'), <Input style={{ width: 320 }} value={publicUrl} onChange={(e) => setPublicUrl(e.target.value)} placeholder="https://portal.example.com" />)}
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('email.publicUrlHint')}
        </Typography.Text>
        <Button type="primary" loading={saving} onClick={save}>
          {t('common.save')}
        </Button>

        <Divider style={{ margin: '4px 0' }} orientation="left" plain>
          {t('email.test')}
        </Divider>
        <Space wrap>
          <Input style={{ width: 260 }} value={testTo} onChange={(e) => setTestTo(e.target.value)} placeholder={t('email.testTo')} />
          <Button loading={testing} onClick={sendTest}>
            {t('email.test')}
          </Button>
        </Space>
      </Space>
    </Card>
  )
}
