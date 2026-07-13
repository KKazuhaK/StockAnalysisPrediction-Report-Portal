import { useEffect, useState } from 'react'
import { App, Button, Divider, Form, Input, Radio, Select, Space, Spin, Switch, Typography, Upload } from 'antd'
import { DeleteOutlined, SaveOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { SettingsResp } from '../../api/types'
import { useSite } from '../../site'
import { BrandIcon } from '../../components/icons'
import StickyActionBar from '../../components/StickyActionBar'

// tzOptions lists the panel-timezone choices: a "follow system" default plus the
// browser's full IANA zone list (Intl.supportedValuesOf, guarded for older engines).
function tzOptions(systemLabel: string) {
  let zones: string[] = []
  try {
    zones = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.('timeZone') ?? []
  } catch {
    zones = []
  }
  return [{ value: '', label: systemLabel }, ...zones.map((z) => ({ value: z, label: z }))]
}

// Site branding, PWA, footer, and panel timezone. Announcement lives on its own
// page now; each page posts only its own fields and the settings API merges
// per-field (nil = untouched), so saving here never disturbs the announcement.
export default function SiteSettingsPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { refresh } = useSite()
  const [form] = Form.useForm()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    api
      .get<SettingsResp>('/api/admin/settings')
      .then((r) =>
        form.setFieldsValue({
          siteTitle: r.siteTitle || '',
          siteLogoUrl: r.siteLogoUrl || '',
          footerText: r.footerText || '',
          footerShowInfo: r.footerShowInfo !== false,
          footerShowVersion: r.footerShowVersion !== false,
          pwaEnabled: r.pwaEnabled !== false,
          pwaIconUrl: r.pwaIconUrl || '',
          timezone: r.timezone || '',
        }),
      )
      .finally(() => setLoading(false))
  }, [form])

  const save = async () => {
    const v = await form.validateFields()
    setSaving(true)
    try {
      await api.post('/api/admin/settings', {
        siteTitle: v.siteTitle || '',
        siteLogoUrl: v.siteLogoUrl || '',
        footerText: v.footerText || '',
        footerShowInfo: v.footerShowInfo !== false,
        footerShowVersion: v.footerShowVersion !== false,
        pwaEnabled: v.pwaEnabled !== false,
        pwaIconUrl: v.pwaIconUrl || '',
        timezone: v.timezone || '',
      })
      await refresh()
      message.success(t('common.saved'))
    } finally {
      setSaving(false)
    }
  }

  const uploadAsset = async (kind: 'logo' | 'pwaIcon', file: File, field: 'siteLogoUrl' | 'pwaIconUrl') => {
    const fd = new FormData()
    fd.set('kind', kind)
    fd.set('file', file)
    const r = await api.upload<{ url: string }>('/api/admin/site-asset', fd)
    form.setFieldsValue({ [field]: r.url })
    message.success(t('common.done'))
  }

  const uploadLogo = (file: File) => {
    if (!file.type.startsWith('image/')) {
      message.error(t('settings.logoTypeInvalid'))
      return false
    }
    if (file.size > 512 * 1024) {
      message.error(t('settings.logoTooLarge'))
      return false
    }
    uploadAsset('logo', file, 'siteLogoUrl').catch((e) => message.error(e instanceof Error ? e.message : t('settings.logoReadFailed')))
    return false
  }

  const uploadPwaIcon = (file: File) => {
    if (!file.type.startsWith('image/')) {
      message.error(t('settings.logoTypeInvalid'))
      return false
    }
    if (file.size > 512 * 1024) {
      message.error(t('settings.pwaIconTooLarge'))
      return false
    }
    uploadAsset('pwaIcon', file, 'pwaIconUrl').catch((e) => message.error(e instanceof Error ? e.message : t('settings.logoReadFailed')))
    return false
  }

  if (loading) return <Spin />

  return (
    <Space direction="vertical" size={12} style={{ width: '100%', maxWidth: 720 }}>
      <Form form={form} layout="vertical">
        <Form.Item
          name="siteTitle"
          label={t('settings.siteTitle')}
          rules={[{ max: 80, message: t('settings.siteTitleTooLong') }]}
        >
          <Input maxLength={80} showCount placeholder={t('brand')} />
        </Form.Item>
        <Form.Item name="siteLogoUrl" label={t('settings.logoUrl')} extra={t('settings.logoHint')}>
          <Input.TextArea autoSize={{ minRows: 2, maxRows: 4 }} placeholder={t('settings.logoPlaceholder')} />
        </Form.Item>
        <Space wrap style={{ marginBottom: 16 }}>
          <Upload accept="image/*" showUploadList={false} beforeUpload={uploadLogo}>
            <Button icon={<UploadOutlined />}>{t('settings.logoUpload')}</Button>
          </Upload>
          <Button icon={<DeleteOutlined />} onClick={() => form.setFieldsValue({ siteLogoUrl: '' })}>
            {t('settings.logoClear')}
          </Button>
        </Space>
        <Form.Item shouldUpdate noStyle>
          {({ getFieldValue }) => {
            const logo = String(getFieldValue('siteLogoUrl') || '').trim()
            const title = String(getFieldValue('siteTitle') || '').trim() || t('brand')
            return (
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 20 }}>
                {logo ? (
                  <img src={logo} alt="" style={{ width: 32, height: 32, objectFit: 'contain' }} />
                ) : (
                  <BrandIcon style={{ color: 'var(--ant-color-primary)', fontSize: 32 }} />
                )}
                <Typography.Text strong>{title}</Typography.Text>
              </div>
            )
          }}
        </Form.Item>
        <Form.Item name="pwaEnabled" label={t('settings.pwaEnabled')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="pwaIconUrl" label={t('settings.pwaIconUrl')} extra={t('settings.pwaIconHint')}>
          <Input.TextArea autoSize={{ minRows: 2, maxRows: 4 }} placeholder={t('settings.logoPlaceholder')} />
        </Form.Item>
        <Space wrap style={{ marginBottom: 16 }}>
          <Upload accept="image/*" showUploadList={false} beforeUpload={uploadPwaIcon}>
            <Button icon={<UploadOutlined />}>{t('settings.pwaIconUpload')}</Button>
          </Upload>
          <Button icon={<DeleteOutlined />} onClick={() => form.setFieldsValue({ pwaIconUrl: '' })}>
            {t('settings.pwaIconClear')}
          </Button>
        </Space>
        <Form.Item shouldUpdate noStyle>
          {({ getFieldValue }) => {
            const icon = String(getFieldValue('pwaIconUrl') || getFieldValue('siteLogoUrl') || '').trim()
            return (
              <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 20 }}>
                {icon ? (
                  <img src={icon} alt="" style={{ width: 40, height: 40, objectFit: 'contain', borderRadius: 8 }} />
                ) : (
                  <BrandIcon style={{ color: 'var(--ant-color-primary)', fontSize: 40 }} />
                )}
                <Typography.Text type="secondary">{t('settings.pwaIconPreview')}</Typography.Text>
              </div>
            )
          }}
        </Form.Item>
        <Divider titlePlacement="left" styles={{ content: { marginInlineStart: 0 } }}>
          {t('settings.footerSection')}
        </Divider>
        <Form.Item name="footerShowInfo" label={t('settings.footerShowInfo')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item
          name="footerText"
          label={t('settings.footerText')}
          extra={t('settings.footerTextHint')}
          rules={[{ max: 1000, message: t('settings.footerTextTooLong') }]}
        >
          <Input.TextArea
            maxLength={1000}
            showCount
            autoSize={{ minRows: 2, maxRows: 4 }}
            placeholder={t('settings.footerTextPlaceholder')}
          />
        </Form.Item>
        <Form.Item name="footerShowVersion" label={t('settings.footerShowVersion')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Divider titlePlacement="left" styles={{ content: { marginInlineStart: 0 } }}>
          {t('settings.timeSection')}
        </Divider>
        <Form.Item name="timezone" label={t('settings.timezone')} style={{ marginBottom: 8 }}>
          <Select
            showSearch
            options={tzOptions(t('settings.timezoneSystem'))}
            style={{ width: '100%' }}
            filterOption={(input, opt) => (opt?.label ?? '').toLowerCase().includes(input.toLowerCase())}
          />
        </Form.Item>
        <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
          {t('settings.timezoneHint')}
        </Typography.Paragraph>
        <StickyActionBar>
          <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={save}>
            {t('common.save')}
          </Button>
        </StickyActionBar>
      </Form>
    </Space>
  )
}
