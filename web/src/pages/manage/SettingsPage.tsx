import { useEffect, useState, type CSSProperties } from 'react'
import {
  App,
  Button,
  Card,
  DatePicker,
  Form,
  Input,
  Modal,
  Popconfirm,
  Select,
  Space,
  Spin,
  Statistic,
  Table,
  Tabs,
  Tag,
  Typography,
  Upload,
} from 'antd'
import { CloudSyncOutlined, DeleteOutlined, PlusOutlined, SaveOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { LegacyImportStatus, SettingsResp, TokenRow } from '../../api/types'
import { useSite } from '../../site'
import { BrandIcon } from '../../components/icons'
import Markdown from '../../components/Markdown'
import { specToEndpoints, type ApiEndpoint, type ApiParam, type ApiError } from './openapiDoc'

const SCOPE_COLORS: Record<string, string> = { all: 'gold', ingest: 'blue', query: 'green' }

function LegacyTab() {
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

function TokensTab() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [tokens, setTokens] = useState<TokenRow[]>([])
  const [loading, setLoading] = useState(true)
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState<number[]>([])
  const [form] = Form.useForm()

  const load = () =>
    api
      .get<{ tokens: TokenRow[] }>('/api/admin/tokens')
      .then((r) => setTokens(r.tokens || []))
      .finally(() => setLoading(false))

  useEffect(() => {
    load()
  }, [])

  const openAdd = () => {
    form.resetFields()
    form.setFieldsValue({ scope: 'all' })
    setOpen(true)
  }

  const create = async () => {
    const v = await form.validateFields()
    await api.post('/api/admin/tokens', {
      name: v.name || '',
      scope: v.scope || 'all',
      expires: v.expires ? v.expires.format('YYYY-MM-DD') : '',
    })
    setOpen(false)
    message.success(t('common.done'))
    load()
  }

  const remove = async (id: number) => {
    await api.del(`/api/admin/tokens/${id}`)
    load()
  }

  const removeSelected = async () => {
    await Promise.all(selected.map((id) => api.del(`/api/admin/tokens/${id}`)))
    setSelected([])
    message.success(t('common.done'))
    load()
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Space wrap>
        <Button type="primary" icon={<PlusOutlined />} onClick={openAdd}>
          {t('common.add')}
        </Button>
        {selected.length > 0 && (
          <Popconfirm title={t('common.deleteConfirm')} onConfirm={removeSelected}>
            <Button danger icon={<DeleteOutlined />}>
              {t('common.deleteSelected')} ({selected.length})
            </Button>
          </Popconfirm>
        )}
      </Space>

      <Table<TokenRow>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={tokens}
        pagination={false}
        rowSelection={{ selectedRowKeys: selected, onChange: (keys) => setSelected(keys as number[]) }}
        columns={[
          { title: t('settings.tokenName'), dataIndex: 'name', render: (n: string) => n || '—' },
          {
            title: 'Token',
            dataIndex: 'token',
            render: (tok: string) => (
              <Typography.Text copyable={{ text: tok }} style={{ fontFamily: 'monospace' }}>
                {tok.slice(0, 8)}…{tok.slice(-4)}
              </Typography.Text>
            ),
          },
          {
            title: t('settings.tokenScope'),
            dataIndex: 'scope',
            render: (s: string) => <Tag color={SCOPE_COLORS[s] || 'default'}>{t(`scope.${s}`, s)}</Tag>,
          },
          { title: t('settings.tokenExpires'), dataIndex: 'expires', render: (e: string) => e || '∞' },
          { title: 'last used', dataIndex: 'lastUsed', render: (e: string) => e || '—' },
          {
            title: '',
            width: 60,
            align: 'right',
            render: (_, r) => (
              <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(r.id)}>
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            ),
          },
        ]}
      />

      <Modal
        open={open}
        title={t('settings.tokens')}
        onOk={create}
        onCancel={() => setOpen(false)}
        okText={t('common.add')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={form} layout="vertical" initialValues={{ scope: 'all' }}>
          <Form.Item name="name" label={t('settings.tokenName')}>
            <Input placeholder={t('settings.tokenName')} />
          </Form.Item>
          <Form.Item name="scope" label={t('settings.tokenScope')}>
            <Select
              options={[
                { value: 'all', label: t('scope.all') },
                { value: 'ingest', label: t('scope.ingest') },
                { value: 'query', label: t('scope.query') },
              ]}
            />
          </Form.Item>
          <Form.Item name="expires" label={t('settings.tokenExpires')}>
            <DatePicker style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}

const METHOD_COLORS: Record<string, string> = {
  GET: 'green',
  POST: 'blue',
  PUT: 'gold',
  PATCH: 'orange',
  DELETE: 'red',
}

const CODE_BLOCK: CSSProperties = {
  background: 'rgba(128,128,128,0.12)',
  padding: 12,
  borderRadius: 8,
  overflow: 'auto',
  fontSize: 12,
  lineHeight: 1.6,
  margin: '4px 0 0',
  whiteSpace: 'pre',
}

function EndpointCard({ e }: { e: ApiEndpoint }) {
  return (
    <Card size="small">
      <Space direction="vertical" size={10} style={{ width: '100%' }}>
        <Space wrap align="center">
          <Tag color={METHOD_COLORS[e.method] || 'default'} style={{ fontFamily: 'monospace', fontWeight: 600, margin: 0 }}>
            {e.method}
          </Tag>
          <Typography.Text code copyable={{ text: e.path }} style={{ fontSize: 14 }}>
            {e.path}
          </Typography.Text>
          <Tag>{e.scope}</Tag>
        </Space>
        <Typography.Text type="secondary">{e.summary}</Typography.Text>

        {e.params.length > 0 && (
          <Table<ApiParam>
            size="small"
            pagination={false}
            rowKey={(p) => `${p.in}:${p.name}`}
            dataSource={e.params}
            columns={[
              { title: '参数', dataIndex: 'name', width: 130, render: (n: string) => <Typography.Text code>{n}</Typography.Text> },
              { title: '位置', dataIndex: 'in', width: 64 },
              { title: '类型', dataIndex: 'type', width: 108 },
              {
                title: '必填',
                dataIndex: 'required',
                width: 60,
                align: 'center',
                render: (v: boolean) => (v ? <Tag color="red">必填</Tag> : <Typography.Text type="secondary">—</Typography.Text>),
              },
              { title: '说明', dataIndex: 'desc' },
            ]}
          />
        )}

        <div>
          <Typography.Text strong style={{ fontSize: 12 }}>
            请求示例
          </Typography.Text>
          <pre style={CODE_BLOCK}>{e.requestExample}</pre>
        </div>
        <div>
          <Typography.Text strong style={{ fontSize: 12 }}>
            响应示例
          </Typography.Text>
          <pre style={CODE_BLOCK}>{e.responseExample}</pre>
        </div>

        {e.errors.length > 0 && (
          <Table<ApiError>
            size="small"
            pagination={false}
            rowKey={(er) => String(er.code)}
            dataSource={e.errors}
            columns={[
              { title: '状态码', dataIndex: 'code', width: 80, render: (c: number) => <Tag color="volcano">{c}</Tag> },
              { title: '触发条件', dataIndex: 'when' },
            ]}
          />
        )}

        {e.notes && (
          <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
            {e.notes}
          </Typography.Paragraph>
        )}
      </Space>
    </Card>
  )
}

function ApiDocTab() {
  const [doc, setDoc] = useState<{ conventions: string; endpoints: ApiEndpoint[] } | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    fetch('/api/openapi.json', { credentials: 'same-origin' })
      .then((r) => r.json())
      .then((spec) => setDoc(specToEndpoints(spec, window.location.origin)))
      .catch(() => setFailed(true))
  }, [])

  if (failed) return <Typography.Text type="danger">加载 openapi.json 失败</Typography.Text>
  if (!doc) return <Spin />

  return (
    <Space direction="vertical" size={12} style={{ width: '100%' }}>
      <Space wrap>
        <Tag color="geekblue">OpenAPI 3.1</Tag>
        <Button size="small" href="/api/openapi.json" target="_blank" rel="noreferrer">
          下载 openapi.json
        </Button>
      </Space>
      <Card size="small" title="约定">
        <Markdown md={doc.conventions} />
      </Card>
      {doc.endpoints.map((e) => (
        <EndpointCard key={`${e.method} ${e.path}`} e={e} />
      ))}
    </Space>
  )
}

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

function GeneralTab() {
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
        timezone: v.timezone || '',
      })
      await refresh()
      message.success(t('common.saved'))
    } finally {
      setSaving(false)
    }
  }

  const uploadLogo = (file: File) => {
    if (!file.type.startsWith('image/')) {
      message.error(t('settings.logoTypeInvalid'))
      return false
    }
    if (file.size > 256 * 1024) {
      message.error(t('settings.logoTooLarge'))
      return false
    }
    const reader = new FileReader()
    reader.onload = () => form.setFieldsValue({ siteLogoUrl: String(reader.result || '') })
    reader.onerror = () => message.error(t('settings.logoReadFailed'))
    reader.readAsDataURL(file)
    return false
  }

  if (loading) return <Spin />

  return (
    <Space direction="vertical" size={12} style={{ width: '100%', maxWidth: 560 }}>
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
        <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={save}>
          {t('common.save')}
        </Button>
      </Form>
    </Space>
  )
}

export default function SettingsPage() {
  const { t } = useTranslation()
  return (
    <Card variant="borderless" styles={{ body: { paddingTop: 8 } }}>
      <Tabs
        items={[
          { key: 'general', label: t('settings.general'), children: <GeneralTab /> },
          { key: 'tokens', label: t('settings.tokens'), children: <TokensTab /> },
          { key: 'apidoc', label: t('settings.apidoc'), children: <ApiDocTab /> },
          { key: 'legacy', label: t('settings.legacyTab'), children: <LegacyTab /> },
        ]}
      />
    </Card>
  )
}
