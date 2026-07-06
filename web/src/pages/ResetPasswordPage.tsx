import { useState } from 'react'
import { Button, Card, Form, Input, Result, Space, Typography, theme } from 'antd'
import { LockOutlined } from '@ant-design/icons'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, ApiError } from '../api/client'

// Public page reached from a reset-email link (/reset?token=…). Sets a new password
// against the stateless reset token; the token is single-use (bound to the old hash).
export default function ResetPasswordPage() {
  const { t } = useTranslation()
  const { token: theme_ } = theme.useToken()
  const [params] = useSearchParams()
  const resetToken = params.get('token') || ''
  const navigate = useNavigate()
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState(false)

  const onFinish = async (v: { password: string }) => {
    setErr('')
    setBusy(true)
    try {
      await api.post('/api/password/reset', { token: resetToken, password: v.password })
      setDone(true)
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : t('reset.error'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 20, background: theme_.colorBgLayout }}>
      <Card style={{ width: '100%', maxWidth: 380, boxShadow: theme_.boxShadowSecondary }}>
        {done ? (
          <Result
            status="success"
            title={t('reset.done')}
            extra={
              <Button type="primary" onClick={() => navigate('/login')}>
                {t('reset.toLogin')}
              </Button>
            }
          />
        ) : !resetToken ? (
          <Result status="warning" title={t('reset.noToken')} extra={<Link to="/login">{t('reset.toLogin')}</Link>} />
        ) : (
          <Space direction="vertical" size={18} style={{ width: '100%' }}>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {t('reset.title')}
            </Typography.Title>
            <Form layout="vertical" onFinish={onFinish} requiredMark={false}>
              <Form.Item name="password" label={t('reset.newPassword')} rules={[{ required: true, min: 6, message: t('reset.tooShort') }]}>
                <Input.Password prefix={<LockOutlined />} autoComplete="new-password" />
              </Form.Item>
              <Form.Item
                name="confirm"
                label={t('reset.confirm')}
                dependencies={['password']}
                rules={[
                  { required: true },
                  ({ getFieldValue }) => ({
                    validator: (_, val) =>
                      !val || getFieldValue('password') === val ? Promise.resolve() : Promise.reject(new Error(t('reset.mismatch'))),
                  }),
                ]}
              >
                <Input.Password prefix={<LockOutlined />} autoComplete="new-password" />
              </Form.Item>
              {err && <Typography.Text type="danger">{err}</Typography.Text>}
              <Button type="primary" htmlType="submit" block loading={busy} style={{ marginTop: 8 }}>
                {t('reset.submit')}
              </Button>
            </Form>
          </Space>
        )}
      </Card>
    </div>
  )
}
