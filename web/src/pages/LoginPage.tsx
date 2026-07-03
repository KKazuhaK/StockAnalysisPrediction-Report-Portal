import { useState } from 'react'
import { Button, Card, Form, Input, Segmented, Select, Space, Typography, theme } from 'antd'
import { LockOutlined, UserOutlined } from '@ant-design/icons'
import { Navigate, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../auth'
import { usePrefs } from '../prefs'
import { ApiError } from '../api/client'
import { SiteLogo, useSite } from '../site'
import { AutoIcon, MoonIcon, SunIcon } from '../components/icons'

export default function LoginPage() {
  const { t } = useTranslation()
  const { title } = useSite()
  const { user, loading, login } = useAuth()
  const { mode, setMode, lang, setLang, langs } = usePrefs()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  if (!loading && user) return <Navigate to="/" replace />

  const onFinish = async (v: { username: string; password: string }) => {
    setErr('')
    setBusy(true)
    try {
      await login(v.username, v.password)
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
      <Card style={{ width: '100%', maxWidth: 380, boxShadow: token.boxShadowSecondary }}>
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
        </Space>
      </Card>
    </div>
  )
}
