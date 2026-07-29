import { useCallback, useEffect, useState } from 'react'
import { Button, Input, Space, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'

// The captcha field on a public form.
//
// It renders NOTHING until the server says a captcha is required for this caller, which is what
// makes the after-failures mode work: an ordinary visitor on a quiet portal never sees it, and it
// appears the moment the server starts asking. The parent re-arms it after a refusal by bumping
// `refresh`, because a challenge is consumed on use and a stale one would fail forever.
//
// Only the self-hosted image provider is rendered here. A token provider (Turnstile, reCAPTCHA,
// hCaptcha) needs its own script, which a self-hosted portal behind a restricted network often
// cannot load — so this build ships the provider that always works, and the field explains what to
// do when an admin has configured one of the others.

export interface CaptchaValue {
  captcha_id?: string
  captcha_answer?: string
  captcha_token?: string
}

interface CaptchaState {
  required: boolean
  provider: string
  site_key?: string
  captcha_id?: string
  image?: string
}

export default function CaptchaField({
  context,
  account,
  refresh,
  value,
  onChange,
}: {
  context: 'login' | 'forgot' | 'register'
  account?: string
  refresh: number
  value: CaptchaValue
  onChange: (v: CaptchaValue) => void
}) {
  const { t } = useTranslation()
  const [state, setState] = useState<CaptchaState | null>(null)

  const load = useCallback(() => {
    const q = account ? `&account=${encodeURIComponent(account)}` : ''
    api
      .get<CaptchaState>(`/api/captcha?ctx=${context}${q}`)
      .then((r) => {
        setState(r)
        onChange({ captcha_id: r.captcha_id, captcha_answer: '' })
      })
      .catch(() => setState(null))
    // onChange is a fresh closure on every parent render; depending on it would refetch in a loop.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [context, account, refresh])
  useEffect(load, [load])

  if (!state?.required) return null

  if (state.provider !== 'image') {
    // An admin configured a hosted provider; this build does not embed their script.
    return (
      <Typography.Text type="warning" style={{ display: 'block', marginBottom: 12 }}>
        {t('captcha.providerUnsupported')}
      </Typography.Text>
    )
  }

  return (
    <Space.Compact style={{ width: '100%', marginBottom: 12 }}>
      <Input
        value={value.captcha_answer ?? ''}
        onChange={(e) => onChange({ captcha_id: state.captcha_id, captcha_answer: e.target.value })}
        placeholder={t('captcha.placeholder')}
        inputMode="numeric"
        autoComplete="off"
        maxLength={8}
      />
      {state.image && (
        <img
          src={state.image}
          alt={t('captcha.alt')}
          onClick={load}
          style={{ height: 32, cursor: 'pointer', borderRadius: 6 }}
          title={t('captcha.refresh')}
        />
      )}
      <Button icon={<ReloadOutlined />} onClick={load} title={t('captcha.refresh')} />
    </Space.Compact>
  )
}
