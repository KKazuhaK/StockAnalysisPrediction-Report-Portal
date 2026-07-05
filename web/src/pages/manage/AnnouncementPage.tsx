import { useEffect, useState } from 'react'
import { Alert, App, Button, Form, Input, Select, Space, Spin, Switch, Typography } from 'antd'
import { SaveOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { SettingsResp } from '../../api/types'
import { useSite } from '../../site'
import { announcementAlertType } from '../../components/SiteAnnouncement'

const ANNOUNCEMENT_LEVELS = ['notice', 'success', 'warning', 'error'] as const

// The site-wide announcement banner. Split out of the general settings page so an
// operator can flip the banner without scrolling past branding; the settings API
// merges per-field, so this save never touches the branding fields.
export default function AnnouncementPage() {
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
          announcementEnabled: r.announcementEnabled === true,
          announcementPopup: r.announcementPopup === true,
          announcementLevel: r.announcementLevel || 'notice',
          announcementTitle: r.announcementTitle || '',
          announcementContent: r.announcementContent || '',
        }),
      )
      .finally(() => setLoading(false))
  }, [form])

  const save = async () => {
    const v = await form.validateFields()
    setSaving(true)
    try {
      await api.post('/api/admin/settings', {
        announcementEnabled: v.announcementEnabled === true,
        announcementPopup: v.announcementPopup === true,
        announcementLevel: v.announcementLevel || 'notice',
        announcementTitle: v.announcementTitle || '',
        announcementContent: v.announcementContent || '',
      })
      await refresh()
      message.success(t('common.saved'))
    } finally {
      setSaving(false)
    }
  }

  if (loading) return <Spin />

  return (
    <Space direction="vertical" size={12} style={{ width: '100%', maxWidth: 720 }}>
      <Form form={form} layout="vertical">
        <Form.Item name="announcementEnabled" label={t('settings.announcementEnabled')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="announcementPopup" label={t('settings.announcementPopup')} valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="announcementLevel" label={t('settings.announcementLevel')}>
          <Select
            options={ANNOUNCEMENT_LEVELS.map((level) => ({
              value: level,
              label: t(`settings.announcementLevel.${level}`),
            }))}
          />
        </Form.Item>
        <Form.Item
          name="announcementTitle"
          label={t('settings.announcementTitle')}
          rules={[{ max: 160, message: t('settings.announcementTitleTooLong') }]}
        >
          <Input maxLength={160} showCount placeholder={t('settings.announcementTitlePlaceholder')} />
        </Form.Item>
        <Form.Item
          name="announcementContent"
          label={t('settings.announcementContent')}
          extra={t('settings.announcementHint')}
          rules={[{ max: 2000, message: t('settings.announcementContentTooLong') }]}
        >
          <Input.TextArea
            maxLength={2000}
            showCount
            autoSize={{ minRows: 3, maxRows: 8 }}
            placeholder={t('settings.announcementContentPlaceholder')}
          />
        </Form.Item>
        <Form.Item shouldUpdate noStyle>
          {({ getFieldValue }) => {
            const enabled = getFieldValue('announcementEnabled') === true
            const title = String(getFieldValue('announcementTitle') || '').trim()
            const content = String(getFieldValue('announcementContent') || '').trim()
            if (!enabled || (!title && !content)) return null
            const previewTitle = title ? <Typography.Text style={{ fontWeight: 700 }}>{title}</Typography.Text> : undefined
            const previewContent = content ? (
              <Typography.Text type="secondary" style={{ whiteSpace: 'pre-line', lineHeight: 1.5 }}>
                {content}
              </Typography.Text>
            ) : undefined
            return (
              <Alert
                showIcon
                type={announcementAlertType(getFieldValue('announcementLevel'))}
                message={previewTitle || previewContent}
                description={previewTitle ? previewContent : undefined}
                style={{ borderRadius: 8, paddingBlock: 8, marginBottom: 20 }}
              />
            )
          }}
        </Form.Item>
        <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={save}>
          {t('common.save')}
        </Button>
      </Form>
    </Space>
  )
}
