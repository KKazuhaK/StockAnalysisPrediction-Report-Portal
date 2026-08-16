import { useCallback, useEffect, useState } from 'react'
import {
  Alert,
  App,
  Button,
  Card,
  Form,
  Input,
  List,
  Modal,
  Popconfirm,
  Space,
  Spin,
  Tag,
  Typography,
} from 'antd'
import { KeyOutlined, LockOutlined, SafetyCertificateOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import { useAuth } from '../auth'
import { formatReportDateTime } from '../lib/datetime'
import SSOIcon from '../components/SSOIcon'
import type { StepUpPolicy } from '../api/types'
import { createCredential, passkeySupported } from '../lib/webauthn'

// Self-service account security (ADR 0023). The 2FA, recovery-code and passkey endpoints existed
// with no way for a user to reach them: enrolment was an admin errand. This page is that way in.
//
// Every credential change asks for a proof first — the account password, or a current code once 2FA
// is on — which the client sends in the X-Step-Up-Proof header. The proof is held only for the
// duration of one ceremony and never stored, so a stolen session cannot register an authenticator
// and a change of mind leaves nothing behind.

interface Passkey {
  id: number
  label: string
  created_at?: string
  last_used_at?: string
}

export default function AccountPage() {
  const { t } = useTranslation()
  const { user, name, federated, totpEnabled, passkeyCount, refresh } = useAuth()
  const [passkeys, setPasskeys] = useState<Passkey[]>([])

  const loadPasskeys = useCallback(() => {
    api
      .get<{ passkeys: Passkey[] }>('/api/me/passkeys')
      .then((r) => setPasskeys(r.passkeys ?? []))
      .catch(() => setPasskeys([]))
  }, [])
  useEffect(loadPasskeys, [loadPasskeys])

  return (
    <Space direction="vertical" size="large" style={{ width: '100%', maxWidth: 760 }}>
      <div>
        <Typography.Title level={3} style={{ marginBottom: 4 }}>
          {t('account.title')}
        </Typography.Title>
        <Typography.Text type="secondary">{name || user}</Typography.Text>
      </div>

      {federated && <Alert type="info" showIcon message={t('account.federatedNotice')} />}

      {!federated && <PasswordCard />}
      {!federated && <TwoFactorCard enabled={totpEnabled} onChange={refresh} />}
      <PasskeyCard
        passkeys={passkeys}
        federated={federated}
        totpEnabled={totpEnabled}
        count={passkeyCount}
        onChange={() => {
          loadPasskeys()
          refresh()
        }}
      />
    </Space>
  )
}

// ---------- password ----------

function PasswordCard() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const [busy, setBusy] = useState(false)

  return (
    <Card title={<Space><LockOutlined />{t('account.passwordTitle')}</Space>}>
      <Typography.Paragraph type="secondary">{t('account.passwordHint')}</Typography.Paragraph>
      <Form
        form={form}
        layout="vertical"
        onFinish={async (v) => {
          setBusy(true)
          try {
            await api.post('/api/me/password', { current: v.current, new: v.next })
            message.success(t('account.passwordChanged'))
            form.resetFields()
          } catch (e) {
            message.error(errText(e, t))
          } finally {
            setBusy(false)
          }
        }}
      >
        <Form.Item name="current" label={t('account.currentPassword')} rules={[{ required: true }]}>
          <Input.Password autoComplete="current-password" />
        </Form.Item>
        <Form.Item
          name="next"
          label={t('account.newPassword')}
          rules={[{ required: true }, { min: 12, message: t('account.passwordTooShort') }]}
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item
          name="confirm"
          label={t('account.confirmPassword')}
          dependencies={['next']}
          rules={[
            { required: true },
            ({ getFieldValue }) => ({
              validator: (_, value) =>
                !value || getFieldValue('next') === value
                  ? Promise.resolve()
                  : Promise.reject(new Error(t('account.passwordMismatch'))),
            }),
          ]}
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={busy}>
          {t('account.changePassword')}
        </Button>
        <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0 }}>
          {t('account.passwordRevokes')}
        </Typography.Paragraph>
      </Form>
    </Card>
  )
}

// ---------- two-factor ----------

function TwoFactorCard({ enabled, onChange }: { enabled: boolean; onChange: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [stage, setStage] = useState<'idle' | 'proof' | 'confirm'>('idle')
  const [secret, setSecret] = useState('')
  const [uri, setUri] = useState('')
  const [code, setCode] = useState('')
  const [recovery, setRecovery] = useState<string[]>([])
  const [busy, setBusy] = useState(false)

  // The far side of a trip to the identity provider: the proof is a cookie the browser is already
  // carrying, so the interrupted action runs with an empty typed proof.
  useEffect(() => {
    if (takeStepUpResume() === 'totp-enable') void beginEnrol('')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const beginEnrol = async (proof: string) => {
    setBusy(true)
    try {
      const r = await api.stepUp<{ secret: string; uri: string }>('POST', '/api/me/2fa/setup', proof)
      setSecret(r.secret)
      setUri(r.uri)
      setStage('confirm')
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const confirmEnrol = async () => {
    setBusy(true)
    try {
      const r = await api.post<{ recovery_codes: string[] }>('/api/me/2fa/enable', { code: code.trim() })
      setRecovery(r.recovery_codes ?? [])
      setStage('idle')
      setCode('')
      onChange()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const disable = async (proof: string) => {
    setBusy(true)
    try {
      await api.post('/api/me/2fa/disable', { code: proof })
      message.success(t('account.totpDisabled'))
      onChange()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card
      title={<Space><SafetyCertificateOutlined />{t('account.totpTitle')}</Space>}
      extra={enabled ? <Tag color="green">{t('account.on')}</Tag> : <Tag>{t('account.off')}</Tag>}
    >
      <Typography.Paragraph type="secondary">{t('account.totpHint')}</Typography.Paragraph>
      {enabled ? (
        <Button danger loading={busy} onClick={() => setStage('proof')}>
          {t('account.totpDisable')}
        </Button>
      ) : (
        <Button type="primary" loading={busy} onClick={() => setStage('proof')}>
          {t('account.totpEnable')}
        </Button>
      )}

      <ProofModal
        open={stage === 'proof'}
        totpEnabled={enabled}
        resume={enabled ? undefined : 'totp-enable'}
        onCancel={() => setStage('idle')}
        onSubmit={async (proof) => {
          setStage('idle')
          if (enabled) await disable(proof)
          else await beginEnrol(proof)
        }}
      />

      <Modal
        open={stage === 'confirm'}
        title={t('account.totpScanTitle')}
        okText={t('account.totpConfirm')}
        confirmLoading={busy}
        onOk={confirmEnrol}
        onCancel={() => {
          setStage('idle')
          setCode('')
        }}
      >
        <Typography.Paragraph>{t('account.totpScanHint')}</Typography.Paragraph>
        <Typography.Paragraph copyable={{ text: uri }} style={{ wordBreak: 'break-all' }}>
          <Typography.Text code>{secret}</Typography.Text>
        </Typography.Paragraph>
        <Input
          value={code}
          onChange={(e) => setCode(e.target.value)}
          placeholder={t('account.totpCodePlaceholder')}
          maxLength={10}
          autoComplete="one-time-code"
          inputMode="numeric"
        />
      </Modal>

      <Modal
        open={recovery.length > 0}
        title={t('account.recoveryTitle')}
        okText={t('account.recoverySaved')}
        onOk={() => setRecovery([])}
        onCancel={() => setRecovery([])}
        cancelButtonProps={{ style: { display: 'none' } }}
      >
        <Alert type="warning" showIcon message={t('account.recoveryWarn')} style={{ marginBottom: 12 }} />
        <Typography.Paragraph copyable={{ text: recovery.join('\n') }}>
          {recovery.map((c) => (
            <div key={c}>
              <Typography.Text code>{c}</Typography.Text>
            </div>
          ))}
        </Typography.Paragraph>
      </Modal>
    </Card>
  )
}

// ---------- passkeys ----------

function PasskeyCard({
  passkeys,
  federated,
  totpEnabled,
  count,
  onChange,
}: {
  passkeys: Passkey[]
  federated: boolean
  totpEnabled: boolean
  count: number
  onChange: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [proofFor, setProofFor] = useState<'register' | number | null>(null)
  const [busy, setBusy] = useState(false)
  const supported = passkeySupported()

  useEffect(() => {
    if (takeStepUpResume() === 'passkey-register') void register('')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const register = async (proof: string) => {
    setBusy(true)
    try {
      const begin = await api.stepUp<{ token: string; options: { publicKey: any } }>(
        'POST',
        '/api/me/passkeys/register/begin',
        proof,
      )
      const attestation = await createCredential(begin.options.publicKey ?? begin.options)
      const label = navigator.platform || 'Passkey'
      await api.post(
        `/api/me/passkeys/register/finish?token=${encodeURIComponent(begin.token)}&label=${encodeURIComponent(label)}`,
        attestation,
      )
      message.success(t('account.passkeyAdded'))
      onChange()
    } catch (e) {
      // A user who dismisses the browser prompt is not an error worth shouting about.
      if (e instanceof DOMException && (e.name === 'NotAllowedError' || e.name === 'AbortError')) return
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  const revoke = async (id: number, proof: string) => {
    setBusy(true)
    try {
      await api.stepUp('DELETE', `/api/me/passkeys/${id}`, proof)
      message.success(t('account.passkeyRemoved'))
      onChange()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card
      title={<Space><KeyOutlined />{t('account.passkeyTitle')}</Space>}
      extra={<Tag>{t('account.passkeyCount', { count })}</Tag>}
      // A federated account has no local factor to step up with, so it cannot add one either.
      // Saying so is kinder than a 403 on submit.
    >
      <Typography.Paragraph type="secondary">{t('account.passkeyHint')}</Typography.Paragraph>
      {!supported && <Alert type="warning" showIcon message={t('account.passkeyUnsupported')} />}
      {federated && <Alert type="info" showIcon message={t('account.passkeyFederated')} />}
      {passkeys.length > 0 && (
        <List
          size="small"
          dataSource={passkeys}
          style={{ marginBottom: 12 }}
          renderItem={(k) => (
            <List.Item
              actions={[
                <Popconfirm
                  key="del"
                  title={t('account.passkeyRevokeConfirm')}
                  onConfirm={() => setProofFor(k.id)}
                >
                  <Button type="text" danger size="small">
                    {t('account.passkeyRevoke')}
                  </Button>
                </Popconfirm>,
              ]}
            >
              <List.Item.Meta
                title={k.label}
                description={
                  k.last_used_at
                    ? t('account.passkeyLastUsed', { when: formatReportDateTime(k.last_used_at) })
                    : t('account.passkeyNeverUsed')
                }
              />
            </List.Item>
          )}
        />
      )}
      <Button
        type="primary"
        disabled={!supported || federated}
        loading={busy}
        onClick={() => setProofFor('register')}
      >
        {t('account.passkeyAdd')}
      </Button>

      <ProofModal
        open={proofFor !== null}
        totpEnabled={totpEnabled}
        resume={proofFor === 'register' ? 'passkey-register' : undefined}
        onCancel={() => setProofFor(null)}
        onSubmit={async (proof) => {
          const target = proofFor
          setProofFor(null)
          if (target === 'register') await register(proof)
          else if (typeof target === 'number') await revoke(target, proof)
        }}
      />
    </Card>
  )
}

// ---------- step-up prompt ----------

// resumeKey remembers which action was interrupted by a trip to the identity provider, so the page
// can finish it on the way back instead of asking the user to find the button again. sessionStorage,
// not a URL: it is scoped to this tab, dies with it, and the round-trip stays free of anything that
// looks like a token to a proxy log.
const resumeKey = 'rp_stepup_resume'

/** Read and clear the pending action, if this load is the far side of a step-up round-trip. */
export function takeStepUpResume(): string | null {
  try {
    const v = sessionStorage.getItem(resumeKey)
    if (v) sessionStorage.removeItem(resumeKey)
    return v
  } catch {
    return null // private mode / storage disabled: the action is simply not resumed
  }
}

// ProofModal collects the re-proved factor for one action. A typed value lives in this component's
// state and dies with it: nothing is cached for "next time", because a cached proof is exactly the
// stolen session the step-up exists to stop.
//
// What it offers is decided by the SERVER (/api/account/stepup/policy), not by what the account
// happens to have. Under force-SSO the portal has stopped accepting a password at the front door,
// so the password box is not drawn here either — and the dialog says why rather than silently
// bouncing the user to a provider they did not ask for.
function ProofModal({
  open,
  totpEnabled,
  resume,
  onCancel,
  onSubmit,
}: {
  open: boolean
  totpEnabled: boolean
  resume?: string
  onCancel: () => void
  onSubmit: (proof: string) => void | Promise<void>
}) {
  const { t } = useTranslation()
  const [proof, setProof] = useState('')
  const [policy, setPolicy] = useState<StepUpPolicy | null>(null)

  useEffect(() => {
    if (!open) return
    api
      .get<StepUpPolicy>('/api/account/stepup/policy')
      // A policy we cannot read must not hide the password box: failing closed here would strand
      // an account whose only way through is the one we just refused to draw.
      .then(setPolicy)
      .catch(() => setPolicy({ password: true, sso: false }))
  }, [open])

  const close = () => {
    setProof('')
    onCancel()
  }

  // A full page navigation, deliberately: the round-trip is the browser's, not fetch's.
  const goToProvider = (kind: string, slug: string) => {
    try {
      if (resume) sessionStorage.setItem(resumeKey, resume)
    } catch {
      /* storage disabled — the trip still works, it just will not resume itself */
    }
    const next = encodeURIComponent(window.location.pathname)
    window.location.href = `/api/auth/${kind}/${slug}/start?purpose=step_up&next=${next}`
  }

  const password = policy?.password ?? true

  return (
    <Modal
      open={open}
      title={t('account.confirmTitle')}
      okText={t('account.confirmOk')}
      okButtonProps={{ disabled: !proof.trim() }}
      onCancel={close}
      onOk={async () => {
        const v = proof.trim()
        setProof('')
        await onSubmit(v)
      }}
      // With no password channel there is nothing for the OK button to submit: the only way on is
      // the provider button below, and an enabled-looking confirm would be a dead end.
      footer={password ? undefined : null}
      destroyOnHidden
    >
      {policy == null ? (
        // The default `password: true` below is a sensible fallback for a policy we could not
        // read, but drawn straight away it also hides the SSO buttons — so an account whose only
        // way through is a provider is shown a password box and nothing else, until the answer
        // lands. A moment's spinner says the same thing without deciding it.
        <div style={{ display: 'grid', justifyItems: 'center', gap: 12, padding: 24 }}>
          <Spin />
          <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
        </div>
      ) : (
      <>
      <Typography.Paragraph type="secondary">
        {!password
          ? t('account.confirmSSOOnly')
          : totpEnabled
            ? t('account.confirmWithCode')
            : t('account.confirmWithPassword')}
      </Typography.Paragraph>
      {password &&
        (totpEnabled ? (
          <Input
            value={proof}
            onChange={(e) => setProof(e.target.value)}
            placeholder={t('account.totpCodePlaceholder')}
            autoComplete="one-time-code"
            inputMode="numeric"
          />
        ) : (
          <Input.Password
            value={proof}
            onChange={(e) => setProof(e.target.value)}
            autoComplete="current-password"
          />
        ))}
      {policy?.sso && (policy.providers ?? []).length > 0 && (
        <div style={{ marginTop: password ? 14 : 0 }}>
          {password && (
            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 6 }}>
              {t('account.confirmOrSSO')}
            </Typography.Text>
          )}
          {/* The admin-configured icon, the same one the login page draws. A hard-coded glyph here
              would make one provider look like two different things depending on the screen. */}
          <Space wrap>
            {(policy.providers ?? []).map((p) => (
              <Button key={p.slug} onClick={() => goToProvider(p.kind, p.slug)}>
                <Space size={8}>
                  <SSOIcon icon={p.icon} />
                  {t('account.confirmViaSSO', { name: p.name })}
                </Space>
              </Button>
            ))}
          </Space>
        </div>
      )}
      </>
      )}
    </Modal>
  )
}
