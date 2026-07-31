import { useEffect, useState } from 'react'
import { Button, Card, Result, Spin } from 'antd'
import { useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'

// The landing page for the emailed confirmation link. It posts the token once and reports the
// outcome; the link is single-use on the server, so a second visit legitimately fails and says so
// rather than looking broken.

export default function VerifyEmailPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [sp] = useSearchParams()
  const [state, setState] = useState<'working' | 'ok' | 'bad'>('working')
  const [err, setErr] = useState('')

  useEffect(() => {
    const token = sp.get('token') || ''
    if (!token) {
      setState('bad')
      setErr(t('verify.missing'))
      return
    }
    api
      .post('/api/register/verify', { token })
      .then(() => setState('ok'))
      .catch((e) => {
        setErr(errText(e, t, 'verify.failed'))
        setState('bad')
      })
  }, [sp, t])

  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', padding: 24 }}>
      <Card style={{ width: 460, maxWidth: '100%' }}>
        {state === 'working' && <Spin />}
        {state === 'ok' && (
          <Result
            status="success"
            title={t('verify.okTitle')}
            subTitle={t('verify.ok')}
            extra={
              <Button type="primary" onClick={() => navigate('/login')}>
                {t('register.backToLogin')}
              </Button>
            }
          />
        )}
        {state === 'bad' && (
          <Result
            status="error"
            title={t('verify.badTitle')}
            subTitle={err}
            extra={
              <Button onClick={() => navigate('/login')}>{t('register.backToLogin')}</Button>
            }
          />
        )}
      </Card>
    </div>
  )
}
