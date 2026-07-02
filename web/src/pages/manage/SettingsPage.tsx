import { useEffect, useState } from 'react'
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
  Statistic,
  Table,
  Tabs,
  Tag,
  Typography,
} from 'antd'
import { CloudSyncOutlined, DeleteOutlined, PlusOutlined, SaveOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { SettingsResp, TokenRow } from '../../api/types'

const SCOPE_COLORS: Record<string, string> = { all: 'gold', ingest: 'blue', query: 'green' }

function SyncTab() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<SettingsResp | null>(null)
  const [form] = Form.useForm()

  const load = () =>
    api.get<SettingsResp>('/api/admin/settings').then((r) => {
      setData(r)
      form.setFieldsValue({ oldBase: r.oldBase, oldUser: r.oldUser, syncMin: r.syncMin })
    })

  useEffect(() => {
    load()
  }, [])

  const save = async () => {
    const v = await form.validateFields()
    await api.post('/api/admin/settings', v)
    message.success(t('common.saved'))
    load()
  }

  const syncNow = async () => {
    await api.post('/api/admin/settings/sync')
    message.info(t('settings.syncStarted'))
  }

  return (
    <Space direction="vertical" size={20} style={{ width: '100%', maxWidth: 560 }}>
      <Space size={40}>
        <Statistic title={t('src.new')} value={data?.newCount ?? 0} />
        <Statistic title={t('src.old')} value={data?.oldCount ?? 0} />
      </Space>
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
        <Form.Item name="syncMin" label={t('settings.syncMin')}>
          <Input style={{ width: 120 }} />
        </Form.Item>
        <Space>
          <Button type="primary" icon={<SaveOutlined />} onClick={save}>
            {t('common.save')}
          </Button>
          <Button icon={<CloudSyncOutlined />} onClick={syncNow}>
            {t('settings.syncNow')}
          </Button>
        </Space>
      </Form>
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

const INGEST_BODY = `{
  "symbol": "002594",
  "name": "比亚迪",
  "date": "2024-01-01",
  "kind": "投资决策",
  "subtype": "汇总",
  "title": "比亚迪 投资决策汇总",
  "body_md": "# 结论\\n**买入**。",
  "run_id": "batch-2024-01",
  "source": "Dify",
  "tracking": [
    { "itype": "assumption", "content": "毛利率维持 20%", "status": "pending", "review_point": "下季度财报" }
  ]
}`

function ApiDocTab() {
  const base = window.location.origin
  const endpoints: [string, string][] = [
    ['POST /api/reports', '入库一篇报告（可带 name / tracking[]），同键覆盖。scope: ingest'],
    ['GET /api/reports', '查/搜历史报告（symbol|q 至少一个）。scope: query'],
    ['GET /api/reports/manifest?symbol=', '某标的报告清单（日期/大类/小文档/计数）'],
    ['GET /api/report?uid=', '取单篇完整正文'],
    ['GET /api/runs?symbol=&date=', '报告组视图（标的+日期+大类）'],
    ['GET /api/symbols?q=&limit=', '有报告的股票清单/补全（按代码或名字）'],
    ['GET /api/tracking?symbol=&status=', '结构化假设/跟踪项（重跑复核）'],
  ]
  return (
    <Space direction="vertical" size={12} style={{ width: '100%' }}>
      <Typography.Paragraph type="secondary">
        全部接口需请求头 <Typography.Text code>Authorization: Bearer &lt;令牌&gt;</Typography.Text>。 Base:{' '}
        <Typography.Text code copyable>
          {base}
        </Typography.Text>
      </Typography.Paragraph>
      <Table
        rowKey={(r) => r[0]}
        size="small"
        pagination={false}
        dataSource={endpoints}
        columns={[
          {
            title: 'Endpoint',
            render: (_: any, r: [string, string]) => (
              <Typography.Text code copyable={{ text: r[0] }}>
                {r[0]}
              </Typography.Text>
            ),
          },
          { title: '说明', render: (_: any, r: [string, string]) => r[1] },
        ]}
      />

      <Typography.Text strong>
        入库请求体示例 · POST /api/reports{' '}
        <Typography.Text
          type="secondary"
          copyable={{ text: INGEST_BODY }}
          style={{ fontWeight: 400, fontSize: 12 }}
        >
          （复制）
        </Typography.Text>
      </Typography.Text>
      <pre
        style={{
          background: 'rgba(128,128,128,0.12)',
          padding: 12,
          borderRadius: 8,
          overflow: 'auto',
          fontSize: 12,
          margin: 0,
        }}
      >
        {INGEST_BODY}
      </pre>
      <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
        · 只 <Typography.Text code>symbol</Typography.Text> / <Typography.Text code>date</Typography.Text> 必填。
        <br />· <Typography.Text code>name</Typography.Text>：可选，入库当时的公司名快照。借壳/改名后老报告仍显示当时名；不传则取名录里的现名。
        <br />· <Typography.Text code>kind</Typography.Text>（大类）不传则按 subtype 推断；身份键 = <Typography.Text code>symbol|date|kind|subtype</Typography.Text>，同键覆盖更新。
      </Typography.Paragraph>
    </Space>
  )
}

export default function SettingsPage() {
  const { t } = useTranslation()
  return (
    <Card variant="borderless" styles={{ body: { paddingTop: 8 } }}>
      <Tabs
        items={[
          { key: 'sync', label: t('settings.oldBase'), children: <SyncTab /> },
          { key: 'tokens', label: t('settings.tokens'), children: <TokensTab /> },
          { key: 'apidoc', label: t('settings.apidoc'), children: <ApiDocTab /> },
        ]}
      />
    </Card>
  )
}
