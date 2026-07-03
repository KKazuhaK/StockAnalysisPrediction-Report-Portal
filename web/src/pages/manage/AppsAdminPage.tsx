import { useEffect, useState } from 'react'
import { App, Button, Card, Popconfirm, Space, Table, Tag, Typography, Upload } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { AppstoreOutlined, CloudDownloadOutlined, DeleteOutlined, ShopOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { AppsResp, AppSummary } from '../../api/types'

export default function AppsAdminPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [apps, setApps] = useState<AppSummary[]>([])
  const [uploading, setUploading] = useState(false)

  const load = () => api.get<AppsResp>('/api/apps').then((r) => setApps(r.apps || []))
  useEffect(() => {
    load()
  }, [])

  // Upload a .zip bundle. The api client only speaks JSON, so post multipart with
  // a raw fetch (same-origin session cookie carries the auth).
  const install = async (file: File) => {
    setUploading(true)
    try {
      const fd = new FormData()
      fd.append('bundle', file)
      const res = await fetch('/api/admin/apps/install', { method: 'POST', body: fd, credentials: 'same-origin' })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        message.error(t('apps.msgInstallFailed', { error: data.error || res.status }))
      } else {
        message.success(t('apps.msgInstalled'))
        load()
      }
    } catch (e) {
      message.error(t('apps.msgInstallFailed', { error: String(e) }))
    } finally {
      setUploading(false)
    }
  }

  const remove = async (id: string) => {
    await api.del(`/api/admin/apps/${id}`)
    load()
  }

  const cols: ColumnsType<AppSummary> = [
    {
      title: t('common.name'),
      render: (_: unknown, a: AppSummary) => (
        <Space>
          {a.icon && <span>{a.icon}</span>}
          <span>{a.name}</span>
          <Typography.Text type="secondary" copyable>
            {a.id}
          </Typography.Text>
        </Space>
      ),
    },
    { title: t('apps.version'), dataIndex: 'version', width: 100, render: (v: string) => v || '—' },
    {
      title: t('apps.scopes'),
      dataIndex: 'scopes',
      width: 180,
      render: (s: string[]) => (s && s.length ? s.map((x) => <Tag key={x}>{x}</Tag>) : <Tag>—</Tag>),
    },
    {
      title: t('batch.col.actions'),
      width: 90,
      render: (_: unknown, a: AppSummary) => (
        <Popconfirm title={t('apps.deleteConfirm')} onConfirm={() => remove(a.id)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <AppstoreOutlined />
            {t('apps.adminTitle')}
          </Space>
        }
        extra={
          <Upload
            accept=".zip"
            showUploadList={false}
            beforeUpload={(file) => {
              install(file as File)
              return Upload.LIST_IGNORE
            }}
          >
            <Button type="primary" icon={<UploadOutlined />} loading={uploading}>
              {t('apps.upload')}
            </Button>
          </Upload>
        }
      >
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('apps.adminHint')}</Typography.Text>
          <Table rowKey="id" size="small" dataSource={apps} columns={cols} pagination={false} />
        </Space>
      </Card>

      {/* App market — a GitHub-hosted one-click install is planned (ADR 0003 phase 2). */}
      <Card
        title={
          <Space>
            <ShopOutlined />
            {t('apps.market')}
            <Tag>{t('apps.comingSoon')}</Tag>
          </Space>
        }
        extra={
          <Button icon={<CloudDownloadOutlined />} disabled>
            {t('apps.marketFetch')}
          </Button>
        }
      >
        <Typography.Text type="secondary">{t('apps.marketHint')}</Typography.Text>
      </Card>
    </Space>
  )
}
