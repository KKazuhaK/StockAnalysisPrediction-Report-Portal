import { useEffect, useState } from 'react'
import { App, Button, Card, Empty, Popconfirm, Space, Table, Tag, Typography, Upload } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { AppstoreOutlined, CloudDownloadOutlined, DeleteOutlined, ShopOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { AppMarketEntry, AppMarketResp, AppPreviewResp, AppsResp, AppSummary } from '../../api/types'
import ScopePermissionModal from '../../components/ScopePermissionModal'

// A pending install awaiting the admin's permission confirmation. `run` performs the
// actual install once the scopes are approved.
interface PendingInstall {
  appName?: string
  scopes: string[]
  run: () => Promise<void>
}

export default function AppsAdminPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [apps, setApps] = useState<AppSummary[]>([])
  const [uploading, setUploading] = useState(false)
  const [market, setMarket] = useState<AppMarketEntry[] | null>(null)
  const [marketLoading, setMarketLoading] = useState(false)
  const [pending, setPending] = useState<PendingInstall | null>(null)
  const [confirming, setConfirming] = useState(false)

  const load = () => api.get<AppsResp>('/api/apps').then((r) => setApps(r.apps || []))
  useEffect(() => {
    load()
  }, [])

  // Post a "bundle" multipart form to the install endpoint. The api client only
  // speaks JSON, so use a raw fetch (the same-origin session cookie carries auth).
  // preview=1 parses without persisting so we can show the permission prompt first.
  const postBundle = async (file: File, preview: boolean) => {
    const fd = new FormData()
    fd.append('bundle', file)
    const url = preview ? '/api/admin/apps/install?preview=1' : '/api/admin/apps/install'
    const res = await fetch(url, { method: 'POST', body: fd, credentials: 'same-origin' })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new Error(data.error || String(res.status))
    return data as AppPreviewResp
  }

  const fetchMarket = async () => {
    setMarketLoading(true)
    try {
      const r = await api.get<AppMarketResp>('/api/admin/apps/market')
      setMarket(r.apps || [])
    } catch (e) {
      message.error(t('apps.marketError', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setMarketLoading(false)
    }
  }

  // Manual upload: preview the bundle to learn its scopes, then queue a confirmed install.
  const install = async (file: File) => {
    setUploading(true)
    try {
      const { app } = await postBundle(file, true)
      setPending({
        appName: app.name,
        scopes: app.scopes || [],
        run: async () => {
          await postBundle(file, false)
          message.success(t('apps.msgInstalled'))
          load()
          if (market) fetchMarket()
        },
      })
    } catch (e) {
      message.error(t('apps.msgInstallFailed', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setUploading(false)
    }
  }

  // Market install: scopes are known from the index entry, so prompt straight away.
  const marketInstall = (entry: AppMarketEntry) => {
    setPending({
      appName: entry.name,
      scopes: entry.scopes || [],
      run: async () => {
        await api.post('/api/admin/apps/market/install', { id: entry.id })
        message.success(t('apps.msgInstalled'))
        load()
        fetchMarket()
      },
    })
  }

  const confirmInstall = async () => {
    if (!pending) return
    setConfirming(true)
    try {
      await pending.run()
      setPending(null)
    } catch (e) {
      message.error(t('apps.msgInstallFailed', { error: e instanceof Error ? e.message : String(e) }))
    } finally {
      setConfirming(false)
    }
  }

  const remove = async (id: string) => {
    await api.del(`/api/admin/apps/${id}`)
    load()
    if (market) fetchMarket()
  }

  const scopeTags = (s?: string[]) => (s && s.length ? s.map((x) => <Tag key={x}>{x}</Tag>) : <Tag>—</Tag>)

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
    { title: t('apps.scopes'), dataIndex: 'scopes', width: 180, render: scopeTags },
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

  const marketCols: ColumnsType<AppMarketEntry> = [
    {
      title: t('common.name'),
      render: (_: unknown, e: AppMarketEntry) => (
        <Space direction="vertical" size={0}>
          <Space>
            {e.icon && <span>{e.icon}</span>}
            <span>{e.name}</span>
            {e.version && <Typography.Text type="secondary">v{e.version}</Typography.Text>}
          </Space>
          {e.description && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {e.description}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    { title: t('apps.scopes'), dataIndex: 'scopes', width: 160, render: scopeTags },
    {
      title: t('batch.col.actions'),
      width: 120,
      render: (_: unknown, e: AppMarketEntry) =>
        e.installed ? (
          <Tag color="green">{t('apps.installedTag')}</Tag>
        ) : (
          <Button size="small" type="primary" icon={<CloudDownloadOutlined />} onClick={() => marketInstall(e)}>
            {t('apps.marketInstall')}
          </Button>
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
          <Table rowKey="id" size="small" dataSource={apps} columns={cols} pagination={false} scroll={{ x: 'max-content' }} />
        </Space>
      </Card>

      {/* App market — one-click install from a GitHub-hosted index (ADR 0003 phase 2). */}
      <Card
        title={
          <Space>
            <ShopOutlined />
            {t('apps.market')}
          </Space>
        }
        extra={
          <Button icon={<CloudDownloadOutlined />} loading={marketLoading} onClick={fetchMarket}>
            {t('apps.marketFetch')}
          </Button>
        }
      >
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('apps.marketHint')}</Typography.Text>
          {market === null ? null : market.length === 0 ? (
            <Empty description={t('apps.marketEmpty')} />
          ) : (
            <Table rowKey="id" size="small" dataSource={market} columns={marketCols} pagination={false} scroll={{ x: 'max-content' }} />
          )}
        </Space>
      </Card>

      <ScopePermissionModal
        open={!!pending}
        appName={pending?.appName}
        scopes={pending?.scopes || []}
        confirmLoading={confirming}
        onConfirm={confirmInstall}
        onCancel={() => setPending(null)}
      />
    </Space>
  )
}
