import { useState } from 'react'
import { Alert, App, Button, Collapse, Select, Space, Steps, Table, Tooltip, Typography } from 'antd'
import { CopyOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'

// A walkthrough for wiring an identity provider to the portal (ADR 0023).
//
// The page already showed every value an admin needs to copy. What it never said is which box on
// the IdP's side each value goes into — and that box has a different name in every product: the
// portal's ACS URL is Entra's "Reply URL", Okta's "Single sign-on URL" and Keycloak's "Valid
// redirect URIs". So the vendor picker here changes only LABELS. The values are properties of this
// portal and never move.
//
// This is documentation, not configuration: nothing here is saved, and the picker is not a claim
// about which IdP is in use.

type Vendor = 'entra' | 'okta' | 'keycloak' | 'google' | 'other'

// Product names and protocol vocabulary, not UI copy — an admin has to match these against what is
// literally printed on the IdP's screen, so translating them would break the only thing they do.
const VENDORS: { value: Vendor; label: string }[] = [
  { value: 'entra', label: 'Microsoft Entra ID' },
  { value: 'okta', label: 'Okta' },
  { value: 'keycloak', label: 'Keycloak' },
  { value: 'google', label: 'Google Workspace' },
  { value: 'other', label: 'Other' },
]

const SAML_FIELDS: Record<'entityId' | 'acs', Record<Vendor, string>> = {
  entityId: {
    entra: 'Identifier (Entity ID)',
    okta: 'Audience URI (SP Entity ID)',
    keycloak: 'Client ID',
    google: 'Entity ID',
    other: 'SP Entity ID / Audience',
  },
  acs: {
    entra: 'Reply URL (Assertion Consumer Service URL)',
    okta: 'Single sign-on URL',
    keycloak: 'Valid redirect URIs',
    google: 'ACS URL',
    other: 'Assertion Consumer Service (ACS) URL',
  },
}

const OIDC_FIELDS: Record<'redirect', Record<Vendor, string>> = {
  redirect: {
    entra: 'Redirect URI (Web)',
    okta: 'Sign-in redirect URI',
    keycloak: 'Valid redirect URIs',
    google: 'Authorized redirect URI',
    other: 'Redirect URI / Callback URL',
  },
}

// Where to find the IdP's own metadata / discovery document, which is the one value that travels in
// the other direction. Empty for "other": inventing a path for an unknown product helps nobody.
const IDP_SOURCE: Record<Vendor, string> = {
  entra: 'Entra ID → Enterprise applications → your app → Single sign-on → SAML Certificates → App Federation Metadata Url',
  okta: 'Okta → Applications → your app → Sign On → Metadata URL',
  keycloak: '<keycloak>/realms/<realm>/protocol/saml/descriptor',
  google: 'Google Admin → Web and mobile apps → your app → Download metadata',
  other: '',
}

const IDP_ISSUER: Record<Vendor, string> = {
  entra: 'https://login.microsoftonline.com/<tenant-id>/v2.0',
  okta: 'https://<your-org>.okta.com/oauth2/default',
  keycloak: '<keycloak>/realms/<realm>',
  google: 'https://accounts.google.com',
  other: '',
}

/** A value the admin pastes into the IdP, next to the name of the box it goes into. */
function ValueRow({ value }: { value: string }) {
  const { message } = App.useApp()
  const { t } = useTranslation()
  return (
    <Space size={4}>
      <Typography.Text code copyable={false} style={{ wordBreak: 'break-all' }}>
        {value}
      </Typography.Text>
      <Tooltip title={t('common.copy')}>
        <Button
          type="text"
          size="small"
          icon={<CopyOutlined />}
          onClick={() => {
            navigator.clipboard?.writeText(value)
            message.success(t('common.copied'))
          }}
        />
      </Tooltip>
    </Space>
  )
}

function FieldTable({ rows }: { rows: { field: string; value: string }[] }) {
  const { t } = useTranslation()
  return (
    <Table
      size="small"
      pagination={false}
      rowKey="field"
      dataSource={rows}
      style={{ marginTop: 8 }}
      columns={[
        { title: t('sso.guide.idpField'), dataIndex: 'field', width: '42%' },
        {
          title: t('sso.guide.pasteThis'),
          dataIndex: 'value',
          render: (v: string) => <ValueRow value={v} />,
        },
      ]}
    />
  )
}

export default function SSOSetupGuide({
  kind,
  values,
  configured,
}: {
  kind: 'oidc' | 'saml'
  /** The portal-side values, already derived from the public URL by the server. */
  values: { entityId?: string; acs?: string; redirect?: string }
  /** True once this provider is enabled and answering — the guide then starts folded. */
  configured: boolean
}) {
  const { t } = useTranslation()
  const [vendor, setVendor] = useState<Vendor>('entra')

  // A value the server derived without a public URL is a bare path. It is copyable and it is
  // wrong, which is the worst combination, so the guide refuses to show any of them.
  const usable = Object.values(values).every((v) => !v || /^https?:\/\//i.test(v))

  const body = !usable ? (
    <Alert type="warning" showIcon message={t('sso.guide.needPublicUrl')} />
  ) : (
    <>
      <Space style={{ marginBottom: 12 }}>
        <Typography.Text type="secondary">{t('sso.guide.vendor')}</Typography.Text>
        <Select value={vendor} onChange={setVendor} options={VENDORS} style={{ width: 200 }} size="small" />
      </Space>
      <Steps
        direction="vertical"
        size="small"
        current={-1}
        items={
          kind === 'saml'
            ? [
                { title: t('sso.guide.saml.step1'), description: t(`sso.guide.saml.step1.${vendor}`) },
                {
                  title: t('sso.guide.saml.step2'),
                  description: (
                    <>
                      <div>{t('sso.guide.saml.step2.body')}</div>
                      <FieldTable
                        rows={[
                          { field: SAML_FIELDS.entityId[vendor], value: values.entityId ?? '' },
                          { field: SAML_FIELDS.acs[vendor], value: values.acs ?? '' },
                        ]}
                      />
                      <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>
                        {t('sso.guide.saml.metadataShortcut')}
                      </Typography.Paragraph>
                    </>
                  ),
                },
                { title: t('sso.guide.saml.step3'), description: t('sso.guide.saml.step3.body') },
                {
                  title: t('sso.guide.saml.step4'),
                  description: (
                    <>
                      <div>{t('sso.guide.saml.step4.body')}</div>
                      {IDP_SOURCE[vendor] && (
                        <Typography.Text code style={{ wordBreak: 'break-all' }}>
                          {IDP_SOURCE[vendor]}
                        </Typography.Text>
                      )}
                    </>
                  ),
                },
                { title: t('sso.guide.step5'), description: t('sso.guide.step5.body') },
              ]
            : [
                { title: t('sso.guide.oidc.step1'), description: t(`sso.guide.oidc.step1.${vendor}`) },
                {
                  title: t('sso.guide.oidc.step2'),
                  description: (
                    <>
                      <div>{t('sso.guide.oidc.step2.body')}</div>
                      <FieldTable rows={[{ field: OIDC_FIELDS.redirect[vendor], value: values.redirect ?? '' }]} />
                    </>
                  ),
                },
                {
                  title: t('sso.guide.oidc.step3'),
                  description: (
                    <>
                      <div>{t('sso.guide.oidc.step3.body')}</div>
                      {IDP_ISSUER[vendor] && (
                        <Typography.Text code style={{ wordBreak: 'break-all' }}>
                          {IDP_ISSUER[vendor]}
                        </Typography.Text>
                      )}
                    </>
                  ),
                },
                { title: t('sso.guide.oidc.step4'), description: t('sso.guide.oidc.step4.body') },
                { title: t('sso.guide.step5'), description: t('sso.guide.step5.body') },
              ]
        }
      />
    </>
  )

  return (
    <Collapse
      style={{ marginBottom: 16 }}
      // Open while there is nothing configured, folded once there is: the guide is for the first
      // time through, and an admin returning to change one attribute should not have to scroll past it.
      defaultActiveKey={configured ? [] : ['guide']}
      items={[{ key: 'guide', label: t('sso.guide.title'), children: body }]}
    />
  )
}
