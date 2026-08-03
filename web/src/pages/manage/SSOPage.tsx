import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, Form, Input, InputNumber, Select, Space, Switch, Tabs, Tag, Typography } from 'antd'
import { CopyOutlined, ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { Role, SSOProviderAdmin, SSOProvidersResp, UserGroupRow, UsersResp } from '../../api/types'
import SSORulesEditor from './SSORulesEditor'
import SSOSetupGuide from './SSOSetupGuide'

// SSO administration (ADR 0023). One SAML tab and one OIDC tab; the API is row-shaped, so adding
// more providers later is a change here and nowhere else.
//
// Two properties this page must preserve: a secret is never displayed (the server only tells us
// whether one is set), and the SP / redirect URLs are read-only, because they are DERIVED from the
// portal's public URL and must match exactly what the server will accept.

const emptyProvider = (kind: 'oidc' | 'saml'): SSOProviderAdmin => ({
  id: 0, kind, slug: kind === 'oidc' ? 'oidc' : 'saml', name: '', enabled: false, provisioning: 'off',
  default_group: 0, default_role: 'user', default_expiry_days: 0, allow_admin_role: false, session_hours: 0,
  issuer: '', client_id: '', scopes: 'openid profile email', has_client_secret: false, redirect_url: '',
  idp_metadata_url: '', idp_entity_id: '', has_idp_metadata: false, allow_idp_initiated: false,
  clock_skew_sec: 60, sp_entity_id: '', sp_acs_url: '', sp_cert_pem: '', sp_cert_not_after: '', has_sp_key: false,
  attr_upn: '', attr_email: '', attr_display: '', attr_groups: '', attr_external_id: '',
})

function CopyField({ label, value, hint }: { label: string; value: string; hint?: string }) {
  const { message } = App.useApp()
  const { t } = useTranslation()
  return (
    <Form.Item label={label} extra={hint}>
      <Space.Compact style={{ width: '100%' }}>
        <Input readOnly value={value} />
        <Button
          icon={<CopyOutlined />}
          onClick={() => {
            navigator.clipboard?.writeText(value)
            message.success(t('common.copied'))
          }}
        />
      </Space.Compact>
    </Form.Item>
  )
}

function ProviderForm({
  kind,
  provider,
  groups,
  publicUrl,
  onSaved,
}: {
  kind: 'oidc' | 'saml'
  provider: SSOProviderAdmin
  groups: UserGroupRow[]
  publicUrl: string
  onSaved: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [form] = Form.useForm()
  const [saving, setSaving] = useState(false)
  // What the last metadata fetch returned, held locally. Reloading the page's providers after a
  // fetch would re-seed this form from the server — and the draft the fetch endpoint creates knows
  // only the URL, so every field the admin had typed and not yet saved came back blank. The button
  // name was the one people noticed; the attribute mappings and the enable switch went the same way.
  const [fetched, setFetched] = useState<{ entity_id: string } | null>(null)

  // Keyed on WHICH provider this is, not on the object. forKind() builds a fresh object on every
  // parent render (it spreads the server defaults over a literal), so depending on the reference
  // re-seeded the form from the server on any re-render at all — including the one antd does to
  // show the success toast, which is why the fetch appeared to clear the fields it never touched.
  const identity = `${provider.kind}:${provider.slug}:${provider.id}`
  useEffect(() => {
    form.setFieldsValue({ ...provider, client_secret: '' })
    setFetched(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, form])

  const hasMetadata = !!fetched || provider.has_idp_metadata
  const metadataEntityID = fetched?.entity_id || provider.idp_entity_id

  const save = async () => {
    const v = await form.validateFields()
    setSaving(true)
    try {
      // An untouched secret field is omitted entirely, so the stored value is kept.
      const body: Record<string, unknown> = { ...v, kind, slug: provider.slug }
      if (!v.client_secret) delete body.client_secret
      await api.post('/api/admin/sso/providers', body)
      message.success(t('common.saved'))
      onSaved()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setSaving(false)
    }
  }

  const fetchMetadata = async () => {
    try {
      // The URL as it is in the FORM, not as it was last saved — this button is normally pressed
      // before anything has been saved at all, and after editing the field it must fetch what the
      // admin is looking at. The kind lets the server create the draft the metadata lands in.
      const r = await api.post<{ entity_id: string }>(
        `/api/admin/sso/providers/${encodeURIComponent(provider.slug)}/metadata`,
        { kind, idp_metadata_url: (form.getFieldValue('idp_metadata_url') || '').trim() },
      )
      message.success(t('sso.metadataFetched', { id: r.entity_id }))
      // Deliberately NOT onSaved(): the fetch's own answer is all the confirmation this needs, and
      // reloading is exactly what discarded the form.
      setFetched({ entity_id: r.entity_id })
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  return (
    <Form form={form} layout="vertical" style={{ maxWidth: 720 }}>
      {!publicUrl && <Alert type="warning" showIcon style={{ marginBottom: 16 }} message={t('sso.needPublicUrl')} />}

      {/* Above everything, because the first question an admin has on this page is not "what is my
          ACS URL" — the page already answered that — but "which of the IdP's boxes does it go in". */}
      <SSOSetupGuide
        kind={kind}
        values={
          kind === 'saml'
            ? { entityId: provider.sp_entity_id, acs: provider.sp_acs_url }
            : { redirect: provider.redirect_url }
        }
      />

      <Form.Item name="enabled" valuePropName="checked" label={t('sso.enable')} extra={t('sso.enableHint')}>
        <Switch />
      </Form.Item>
      <Form.Item name="name" label={t('sso.displayName')} extra={t('sso.displayNameHint')}>
        <Input placeholder={kind === 'oidc' ? 'Entra ID' : 'Corporate SSO'} />
      </Form.Item>

      {kind === 'oidc' ? (
        <>
          <Form.Item name="issuer" label={t('sso.issuer')} extra={t('sso.issuerHint')}>
            <Input placeholder="https://login.microsoftonline.com/<tenant>/v2.0" />
          </Form.Item>
          <Form.Item name="client_id" label={t('sso.clientId')}>
            <Input />
          </Form.Item>
          <Form.Item
            name="client_secret"
            label={t('sso.clientSecret')}
            extra={provider.has_client_secret ? t('sso.secretKeepHint') : t('sso.secretHint')}
          >
            <Input.Password placeholder={provider.has_client_secret ? '••••••••' : ''} autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="scopes" label={t('sso.scopes')} extra={t('sso.scopesHint')}>
            <Input placeholder="openid profile email" />
          </Form.Item>
          <CopyField label={t('sso.redirectUrl')} value={provider.redirect_url} hint={t('sso.redirectUrlHint')} />
        </>
      ) : (
        <>
          <Form.Item name="idp_metadata_url" label={t('sso.metadataUrl')} extra={t('sso.metadataUrlHint')}>
            <Input placeholder="https://login.microsoftonline.com/<tenant>/federationmetadata/..." />
          </Form.Item>
          <Space style={{ marginBottom: 16 }}>
            <Button icon={<ReloadOutlined />} onClick={fetchMetadata}>
              {t('sso.fetchMetadata')}
            </Button>
            {hasMetadata ? (
              <Tag color="green">{metadataEntityID || t('sso.metadataPresent')}</Tag>
            ) : (
              <Tag>{t('sso.metadataMissing')}</Tag>
            )}
          </Space>
          <Typography.Title level={5}>{t('sso.spTitle')}</Typography.Title>
          <Typography.Paragraph type="secondary">{t('sso.spHint')}</Typography.Paragraph>
          <CopyField label={t('sso.spEntityId')} value={provider.sp_entity_id} />
          <CopyField label={t('sso.spAcsUrl')} value={provider.sp_acs_url} />
          {provider.sp_cert_pem && (
            <CopyField
              label={t('sso.spCert')}
              value={provider.sp_cert_pem}
              hint={provider.sp_cert_not_after ? t('sso.spCertExpires', { at: provider.sp_cert_not_after }) : undefined}
            />
          )}
          <Form.Item
            name="allow_idp_initiated"
            valuePropName="checked"
            label={t('sso.allowIdpInitiated')}
            extra={t('sso.allowIdpInitiatedHint')}
          >
            <Switch />
          </Form.Item>
        </>
      )}

      <Typography.Title level={5}>{t('sso.mappingTitle')}</Typography.Title>
      <Typography.Paragraph type="secondary">{t('sso.mappingHint')}</Typography.Paragraph>
      <Form.Item name="attr_upn" label={t('sso.attrUpn')} extra={t('sso.attrUpnHint')}>
        <Input placeholder={kind === 'saml' ? 'nameid' : 'preferred_username'} />
      </Form.Item>
      <Form.Item name="attr_email" label={t('sso.attrEmail')}>
        <Input placeholder="email" />
      </Form.Item>
      <Form.Item name="attr_display" label={t('sso.attrDisplay')}>
        <Input placeholder="name" />
      </Form.Item>
      <Form.Item name="attr_groups" label={t('sso.attrGroups')} extra={t('sso.attrGroupsHint')}>
        <Input placeholder="groups" />
      </Form.Item>
      <Form.Item name="attr_external_id" label={t('sso.attrExternalId')} extra={t('sso.attrExternalIdHint')}>
        <Input placeholder="oid" />
      </Form.Item>

      <Typography.Title level={5}>{t('sso.provisioningTitle')}</Typography.Title>
      <Form.Item name="provisioning" label={t('sso.provisioning')} extra={t('sso.provisioningHint')}>
        <Select
          options={[
            { value: 'off', label: t('sso.provisioningOff') },
            { value: 'jit', label: t('sso.provisioningJit') },
          ]}
        />
      </Form.Item>
      <Form.Item name="default_group" label={t('sso.defaultGroup')} extra={t('sso.defaultGroupHint')}>
        <Select
          allowClear
          options={groups.map((g) => ({
            value: g.id,
            label: g.restricted_effective ? `${g.name} · ${t('users.restrictedTag')}` : g.name,
          }))}
        />
      </Form.Item>
      <Form.Item name="default_expiry_days" label={t('sso.defaultExpiry')} extra={t('sso.defaultExpiryHint')}>
        <InputNumber min={0} max={3650} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item name="session_hours" label={t('sso.sessionHours')} extra={t('sso.sessionHoursHint')}>
        <InputNumber min={0} max={720} style={{ width: '100%' }} />
      </Form.Item>
      <Form.Item
        name="allow_admin_role"
        valuePropName="checked"
        label={t('sso.allowAdminRole')}
        extra={t('sso.allowAdminRoleHint')}
      >
        <Switch />
      </Form.Item>

      <Button type="primary" onClick={save} loading={saving}>
        {t('common.save')}
      </Button>
    </Form>
  )
}

export default function SSOPage() {
  const { t } = useTranslation()
  const [providers, setProviders] = useState<SSOProviderAdmin[]>([])
  const [groups, setGroups] = useState<UserGroupRow[]>([])
  const [roles, setRoles] = useState<Role[]>([])
  const [publicUrl, setPublicUrl] = useState('')
  const [spDefaults, setSpDefaults] = useState<NonNullable<SSOProvidersResp['sp_defaults']>>({})

  const load = () => {
    api
      .get<SSOProvidersResp>('/api/admin/sso/providers')
      .then((r) => {
        setProviders(r.providers || [])
        setPublicUrl(r.public_url || '')
        setSpDefaults(r.sp_defaults || {})
      })
      .catch(() => {})
    api
      .get<{ groups: UserGroupRow[] }>('/api/admin/groups')
      .then((r) => setGroups(r.groups || []))
      .catch(() => {})
    // The rules editor assigns roles, and the role registry is server-side (roles.go), so the list
    // has to come from the same place the account page gets it.
    api
      .get<UsersResp>('/api/admin/users')
      .then((r) => setRoles(r.roles || []))
      .catch(() => {})
  }
  useEffect(load, [])

  // A provider that has not been saved has no row, so its SP addresses come from the server's
  // derived defaults instead. Without them the setup guide — the thing an admin reads BEFORE
  // saving anything — showed two empty boxes on precisely the install that had never used SSO.
  const forKind = (kind: 'oidc' | 'saml') =>
    providers.find((p) => p.kind === kind) ?? { ...emptyProvider(kind), ...(spDefaults[kind] ?? {}) }

  return (
    <Card>
      <Tabs
        items={[
          ...(['saml', 'oidc'] as const).map((kind) => ({
            key: kind,
            label: kind === 'saml' ? t('sso.tabSaml') : t('sso.tabOidc'),
            children: (
              <ProviderForm
                kind={kind}
                provider={forKind(kind)}
                groups={groups}
                publicUrl={publicUrl}
                onSaved={load}
              />
            ),
          })),
          {
            key: 'rules',
            label: t('sso.tabRules'),
            children: <SSORulesEditor providers={providers} groups={groups} roles={roles} />,
          },
        ]}
      />
    </Card>
  )
}
