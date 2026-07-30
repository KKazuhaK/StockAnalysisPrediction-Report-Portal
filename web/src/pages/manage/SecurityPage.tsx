import { useCallback, useEffect, useState } from 'react'
import { Alert, App, Button, Card, Divider, Input, InputNumber, Select, Space, Switch, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, ApiError } from '../../api/client'

// Login protection and self-service registration.
//
// The two live on one page because they are one decision: opening a public signup form is what
// makes the captcha matter, and the captcha is what makes the signup form survivable.

interface CaptchaCfg {
  provider: string
  site_key: string
  has_secret: boolean
  login: boolean
  forgot: boolean
  register: boolean
  trigger: string
  fail_threshold: string
}
interface RegCfg {
  enabled: boolean
  require_verify: boolean
  domains: string
  default_group: string
  expiry_days: string
}
// The two login axes (login_mode.go). `effective` is what the portal actually does right now —
// it differs from `mode` when no provider is enabled and the mode degrades — so the admin can be
// told their choice is currently inert instead of wondering why nothing changed.
interface LoginCfg {
  mode: string
  effective: string
  sso_only: boolean
  sso_available: boolean
}

interface GroupRow {
  id: number
  name: string
  restricted?: boolean
}

export default function SecurityPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [captcha, setCaptcha] = useState<CaptchaCfg | null>(null)
  const [reg, setReg] = useState<RegCfg | null>(null)
  const [login, setLogin] = useState<LoginCfg | null>(null)
  const [groups, setGroups] = useState<GroupRow[]>([])
  const [emailOK, setEmailOK] = useState(true)
  const [secret, setSecret] = useState<string | null>(null) // null = leave the stored one alone
  const [busy, setBusy] = useState(false)

  const load = useCallback(() => {
    api
      .get<{
        captcha: CaptchaCfg
        registration: RegCfg
        login: LoginCfg
        groups: GroupRow[]
        email_configured: boolean
      }>('/api/admin/security')
      .then((r) => {
        setCaptcha(r.captcha)
        setReg(r.registration)
        setLogin(r.login)
        setGroups(r.groups ?? [])
        setEmailOK(r.email_configured)
        setSecret(null)
      })
      .catch((e) => message.error(e instanceof ApiError ? e.message : String(e)))
  }, [])
  useEffect(load, [load])

  const save = async () => {
    if (!captcha || !reg || !login) return
    setBusy(true)
    try {
      await api.post('/api/admin/security', {
        captcha: {
          provider: captcha.provider,
          site_key: captcha.site_key,
          // Omitted entirely when untouched, so saving the page never clears a secret the admin
          // cannot see and therefore cannot retype.
          ...(secret === null ? {} : { secret_key: secret }),
          login: captcha.login,
          forgot: captcha.forgot,
          register: captcha.register,
          trigger: captcha.trigger,
          fail_threshold: Number(captcha.fail_threshold) || 3,
        },
        registration: reg,
        login: { mode: login.mode, sso_only: login.sso_only },
      })
      message.success(t('common.saved'))
      load()
    } catch (e) {
      message.error(e instanceof ApiError ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  if (!captcha || !reg || !login) return null
  const tokenProvider = captcha.provider !== 'image'

  return (
    <Space direction="vertical" size="large" style={{ width: '100%', maxWidth: 720 }}>
      <Card title={t('security.loginTitle')}>
        <Row label={t('security.loginMode')} hint={t('security.loginModeHint')}>
          <Select
            style={{ width: 280 }}
            value={login.mode}
            onChange={(v) => setLogin({ ...login, mode: v })}
            options={[
              { value: 'dual', label: t('security.loginDual') },
              { value: 'sso_first', label: t('security.loginSSOFirst') },
              { value: 'sso_redirect', label: t('security.loginSSORedirect') },
              { value: 'local_only', label: t('security.loginLocalOnly') },
            ]}
          />
        </Row>
        <Row label={t('security.ssoOnly')} hint={t('security.ssoOnlyHint')}>
          <Switch checked={login.sso_only} onChange={(v) => setLogin({ ...login, sso_only: v })} />
        </Row>
        {!login.sso_available && (login.mode !== 'local_only' || login.sso_only) && (
          <Alert type="warning" showIcon style={{ marginTop: 12 }} message={t('security.noProviderWarning')} />
        )}
      </Card>

      <Card title={t('security.captchaTitle')}>
        <Typography.Paragraph type="secondary">{t('security.captchaDesc')}</Typography.Paragraph>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          <Row label={t('security.provider')} hint={t('security.providerHint')}>
            <Select
              value={captcha.provider}
              style={{ width: '100%' }}
              onChange={(v) => setCaptcha({ ...captcha, provider: v })}
              options={[
                { value: 'image', label: t('security.providerImage') },
                { value: 'turnstile', label: 'Cloudflare Turnstile' },
                { value: 'recaptcha', label: 'Google reCAPTCHA v2' },
                { value: 'hcaptcha', label: 'hCaptcha' },
              ]}
            />
          </Row>
          {tokenProvider && (
            <>
              <Alert type="warning" showIcon message={t('security.tokenProviderNote')} />
              <Row label={t('security.siteKey')}>
                <Input value={captcha.site_key} onChange={(e) => setCaptcha({ ...captcha, site_key: e.target.value })} />
              </Row>
              <Row label={t('security.secretKey')} hint={t('security.secretKeyHint')}>
                <Input.Password
                  value={secret ?? ''}
                  placeholder={captcha.has_secret ? t('security.secretStored') : t('security.secretEmpty')}
                  onChange={(e) => setSecret(e.target.value)}
                />
              </Row>
            </>
          )}
          <Divider style={{ margin: '4px 0' }} />
          <Row label={t('security.onLogin')} hint={t('security.onLoginHint')}>
            <Switch checked={captcha.login} onChange={(v) => setCaptcha({ ...captcha, login: v })} />
          </Row>
          {captcha.login && (
            <>
              <Row label={t('security.trigger')}>
                <Select
                  value={captcha.trigger}
                  style={{ width: '100%' }}
                  onChange={(v) => setCaptcha({ ...captcha, trigger: v })}
                  options={[
                    { value: 'always', label: t('security.triggerAlways') },
                    { value: 'after_failures', label: t('security.triggerAfterFailures') },
                  ]}
                />
              </Row>
              {captcha.trigger === 'after_failures' && (
                <Row label={t('security.threshold')} hint={t('security.thresholdHint')}>
                  <InputNumber
                    min={1}
                    max={20}
                    value={Number(captcha.fail_threshold) || 3}
                    onChange={(v) => setCaptcha({ ...captcha, fail_threshold: String(v ?? 3) })}
                  />
                </Row>
              )}
            </>
          )}
          <Row label={t('security.onForgot')} hint={t('security.onForgotHint')}>
            <Switch checked={captcha.forgot} onChange={(v) => setCaptcha({ ...captcha, forgot: v })} />
          </Row>
          <Row label={t('security.onRegister')}>
            <Switch checked={captcha.register} onChange={(v) => setCaptcha({ ...captcha, register: v })} />
          </Row>
        </Space>
      </Card>

      <Card title={t('security.regTitle')}>
        <Typography.Paragraph type="secondary">{t('security.regDesc')}</Typography.Paragraph>
        <Space direction="vertical" size="middle" style={{ width: '100%' }}>
          {reg.enabled && reg.require_verify && !emailOK && (
            <Alert type="error" showIcon message={t('security.regNeedsEmail')} />
          )}
          <Row label={t('security.regEnabled')}>
            <Switch checked={reg.enabled} onChange={(v) => setReg({ ...reg, enabled: v })} />
          </Row>
          <Row label={t('security.regVerify')} hint={t('security.regVerifyHint')}>
            <Switch checked={reg.require_verify} onChange={(v) => setReg({ ...reg, require_verify: v })} />
          </Row>
          <Row label={t('security.regDomains')} hint={t('security.regDomainsHint')}>
            <Input
              value={reg.domains}
              placeholder="example.com, corp.example"
              onChange={(e) => setReg({ ...reg, domains: e.target.value })}
            />
          </Row>
          <Row label={t('security.regGroup')} hint={t('security.regGroupHint')}>
            <Select
              value={reg.default_group || ''}
              style={{ width: '100%' }}
              onChange={(v) => setReg({ ...reg, default_group: v })}
              options={[
                { value: '', label: t('security.regGroupNone') },
                ...groups.map((g) => ({
                  value: String(g.id),
                  label: g.restricted ? `${g.name} · ${t('users.restrictedTag')}` : g.name,
                })),
              ]}
            />
          </Row>
          <Row label={t('security.regExpiry')} hint={t('security.regExpiryHint')}>
            <InputNumber
              min={0}
              max={3650}
              value={Number(reg.expiry_days) || 0}
              onChange={(v) => setReg({ ...reg, expiry_days: v ? String(v) : '' })}
            />
          </Row>
        </Space>
      </Card>

      <Button type="primary" loading={busy} onClick={save}>
        {t('common.save')}
      </Button>
    </Space>
  )
}

function Row({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div>
      <Typography.Text strong>{label}</Typography.Text>
      <div style={{ marginTop: 6 }}>{children}</div>
      {hint && (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {hint}
        </Typography.Text>
      )}
    </div>
  )
}
