import { useEffect, useState } from 'react'
import { App, Button, Form, Input, Space, Spin, Statistic, Typography } from 'antd'
import { CloudSyncOutlined, SaveOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { LegacyImportStatus, SettingsResp } from '../../api/types'

// One-shot pull of the old portal's history into the local store. An operational
// maintenance task, not a persistent setting — it lives under Maintenance now.
export default function LegacyImportPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<SettingsResp | null>(null)
  const [status, setStatus] = useState<LegacyImportStatus | null>(null)
  const [form] = Form.useForm()

  const load = () =>
    api.get<SettingsResp>('/api/admin/settings').then((r) => {
      setData(r)
      form.setFieldsValue({ oldBase: r.oldBase, oldUser: r.oldUser })
    })

  const pollStatus = () => api.get<LegacyImportStatus>('/api/admin/legacy/status').then(setStatus).catch(() => {})

  useEffect(() => {
    load()
    pollStatus()
    const id = setInterval(pollStatus, 3000) // keep live count / progress fresh
    return () => clearInterval(id)
  }, [])

  const save = async () => {
    const v = await form.validateFields()
    await api.post('/api/admin/settings', v)
    message.success(t('common.saved'))
    load()
  }

  const runImport = async () => {
    const r = await api.post<{ alreadyRunning?: boolean }>('/api/admin/legacy/import', {})
    message.info(r?.alreadyRunning ? t('settings.importRunning') : t('settings.importStarted'))
    pollStatus()
  }

  const running = !!status?.running
  return (
    <Space direction="vertical" size={20} style={{ width: '100%', maxWidth: 560 }}>
      <Statistic title={t('src.new')} value={status?.count ?? data?.newCount ?? 0} />
      <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
        {t('settings.legacyHint')}
      </Typography.Paragraph>
      <Form form={form} layout="vertical">
        <Form.Item name="oldBase" label={t('settings.oldBase')}>
          <Input placeholder="http://…" />
        </Form.Item>
        <Form.Item name="oldUser" label={t('settings.oldUser')}>
          <Input autoComplete="off" />
        </Form.Item>
        <Form.Item name="oldPass" label={t('settings.oldPass')}>
          <Input.Password
            autoComplete="new-password"
            placeholder={data?.hasPass ? t('settings.oldPassSet') : ''}
          />
        </Form.Item>
        <Space wrap>
          <Button type="primary" icon={<SaveOutlined />} onClick={save}>
            {t('common.save')}
          </Button>
          <Button icon={<CloudSyncOutlined />} loading={running} onClick={runImport}>
            {t('settings.runImport')}
          </Button>
        </Space>
      </Form>
      {status && (running || status.finished) && (
        <Typography.Text type={status.error ? 'danger' : running ? undefined : 'success'}>
          {running ? (
            <Space size={6}>
              <Spin size="small" /> {t('settings.importRunning')} — +{status.imported} / skip {status.skipped} / fail{' '}
              {status.failed}
            </Space>
          ) : (
            <>
              {t('settings.importDone')}: +{status.imported} / skip {status.skipped} / fail {status.failed}
              {status.error ? ` — ${status.error}` : ''}
            </>
          )}
        </Typography.Text>
      )}
    </Space>
  )
}
