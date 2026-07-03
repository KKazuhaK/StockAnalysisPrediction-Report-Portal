import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  Upload,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { DeleteOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { BatchPlugin, BatchTarget } from '../../api/types'

export default function BatchAdminPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()

  const [plugins, setPlugins] = useState<BatchPlugin[]>([])
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [maxConcurrency, setMaxConcurrency] = useState<number>(10)
  const [maxJobs, setMaxJobs] = useState<number>(1)
  const [reservedSlots, setReservedSlots] = useState<number>(1)
  const [ticketPeriod, setTicketPeriod] = useState<number>(7)

  const [targetOpen, setTargetOpen] = useState(false)
  const [form] = Form.useForm()
  const pluginSlug = Form.useWatch('plugin_slug', form)

  const loadPlugins = () =>
    api.get<{ plugins: BatchPlugin[] }>('/api/admin/batch/plugins').then((r) => setPlugins(r.plugins || []))
  const loadTargets = () =>
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || []))
  const loadConfig = () =>
    api
      .get<{ max_concurrency: number; max_jobs: number; reserved_slots: number; ticket_period_days: number }>('/api/admin/batch/config')
      .then((r) => {
        setMaxConcurrency(r.max_concurrency)
        setMaxJobs(r.max_jobs)
        setReservedSlots(r.reserved_slots)
        setTicketPeriod(r.ticket_period_days)
      })

  useEffect(() => {
    loadPlugins()
    loadTargets()
    loadConfig()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const importFile = (file: File) => {
    const reader = new FileReader()
    reader.onload = async () => {
      try {
        const obj = JSON.parse(String(reader.result))
        await api.post('/api/admin/batch/plugins/import', obj)
        message.success(t('batch.admin.msgImported'))
        loadPlugins()
      } catch (e) {
        message.error(`${t('batch.admin.msgImportFailed')}：${(e as Error).message || ''}`)
      }
    }
    reader.readAsText(file)
    return false
  }

  const deletePlugin = async (slug: string) => {
    await api.del(`/api/admin/batch/plugins/${encodeURIComponent(slug)}`)
    loadPlugins()
  }

  const selectedPlugin = useMemo(() => plugins.find((p) => p.slug === pluginSlug), [plugins, pluginSlug])

  const submitTarget = async () => {
    const v = await form.validateFields()
    const config: Record<string, string> = {}
    for (const f of selectedPlugin?.config || []) config[f.key] = v[`cfg_${f.key}`] || ''
    await api.post('/api/admin/batch/targets', { plugin_slug: v.plugin_slug, name: v.name, config })
    setTargetOpen(false)
    message.success(t('batch.admin.msgTargetCreated'))
    loadTargets()
  }

  const deleteTarget = async (id: number) => {
    await api.del(`/api/admin/batch/targets/${id}`)
    loadTargets()
  }

  const saveConfig = async () => {
    await api.post('/api/admin/batch/config', {
      max_concurrency: maxConcurrency,
      max_jobs: maxJobs,
      reserved_slots: reservedSlots,
      ticket_period_days: ticketPeriod,
    })
    message.success(t('common.saved'))
    loadConfig()
  }

  const sourceLabel = (s: string) =>
    s === 'market' ? t('batch.admin.sourceMarket') : s === 'bundled' ? t('batch.admin.sourceBundled') : t('batch.admin.sourceImported')

  const pluginCols: ColumnsType<BatchPlugin> = [
    { title: t('common.name'), dataIndex: 'name' },
    { title: t('batch.admin.slug'), dataIndex: 'slug' },
    { title: t('batch.admin.version'), dataIndex: 'version', width: 90 },
    { title: t('batch.admin.source'), dataIndex: 'source', width: 90, render: (s: string) => <Tag>{sourceLabel(s)}</Tag> },
    {
      title: t('batch.admin.inputs'),
      render: (_: unknown, p: BatchPlugin) => (p.inputs || []).map((i) => <Tag key={i.key}>{i.key}</Tag>),
    },
    {
      title: t('batch.col.actions'),
      width: 80,
      render: (_: unknown, p: BatchPlugin) => (
        <Popconfirm title={t('batch.admin.deletePluginConfirm')} onConfirm={() => deletePlugin(p.slug)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ]

  const targetCols: ColumnsType<BatchTarget> = [
    { title: t('common.name'), dataIndex: 'name' },
    { title: t('batch.admin.plugin'), dataIndex: 'plugin_name' },
    { title: t('batch.admin.createdAt'), dataIndex: 'created_at', width: 170 },
    {
      title: t('batch.col.actions'),
      width: 80,
      render: (_: unknown, tg: BatchTarget) => (
        <Popconfirm title={t('batch.admin.deleteTargetConfirm')} onConfirm={() => deleteTarget(tg.id)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={t('batch.admin.installed')}
        extra={
          <Upload accept=".json" showUploadList={false} beforeUpload={importFile}>
            <Button icon={<UploadOutlined />}>{t('batch.admin.importManifest')}</Button>
          </Upload>
        }
      >
        <Table rowKey="slug" size="small" dataSource={plugins} columns={pluginCols} pagination={false} />
      </Card>

      <Card
        title={t('batch.admin.targets')}
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={plugins.length === 0}
            onClick={() => {
              form.resetFields()
              setTargetOpen(true)
            }}
          >
            {t('batch.admin.newTarget')}
          </Button>
        }
      >
        <Table rowKey="id" size="small" dataSource={targets} columns={targetCols} pagination={false} />
      </Card>

      <Card title={t('batch.admin.settings')}>
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Space wrap>
            <span>{t('batch.admin.maxJobs')}</span>
            <InputNumber min={1} max={50} value={maxJobs} onChange={(v) => setMaxJobs(v || 1)} />
            <Typography.Text type="secondary">{t('batch.admin.maxJobsHint')}</Typography.Text>
          </Space>
          <Space wrap>
            <span>{t('batch.admin.reservedSlots')}</span>
            <InputNumber min={0} max={Math.max(0, maxJobs - 1)} value={reservedSlots} onChange={(v) => setReservedSlots(v ?? 0)} />
            <Typography.Text type="secondary">{t('batch.admin.reservedSlotsHint')}</Typography.Text>
          </Space>
          <Space wrap>
            <span>{t('batch.admin.ticketPeriod')}</span>
            <InputNumber min={1} max={365} value={ticketPeriod} onChange={(v) => setTicketPeriod(v || 7)} addonAfter={t('batch.admin.days')} />
            <Typography.Text type="secondary">{t('batch.admin.ticketPeriodHint')}</Typography.Text>
          </Space>
          <Space wrap>
            <span>{t('batch.admin.maxConcurrency')}</span>
            <InputNumber min={1} max={100} value={maxConcurrency} onChange={(v) => setMaxConcurrency(v || 1)} />
            <Typography.Text type="secondary">{t('batch.admin.maxConcurrencyHint')}</Typography.Text>
          </Space>
          <Button type="primary" onClick={saveConfig}>
            {t('common.save')}
          </Button>
        </Space>
      </Card>

      <Modal title={t('batch.admin.newTargetTitle')} open={targetOpen} onOk={submitTarget} onCancel={() => setTargetOpen(false)} destroyOnClose>
        <Form form={form} layout="vertical">
          <Form.Item name="plugin_slug" label={t('batch.admin.plugin')} rules={[{ required: true, message: t('batch.admin.selectPluginRequired') }]}>
            <Select
              placeholder={t('batch.admin.selectPlugin')}
              options={plugins.map((p) => ({ value: p.slug, label: p.name }))}
            />
          </Form.Item>
          <Form.Item name="name" label={t('batch.admin.targetName')} rules={[{ required: true, message: t('batch.admin.nameRequired') }]}>
            <Input placeholder={t('batch.admin.targetNamePlaceholder')} />
          </Form.Item>
          {(selectedPlugin?.config || []).map((f) => (
            <Form.Item
              key={f.key}
              name={`cfg_${f.key}`}
              label={f.label || f.key}
              rules={[{ required: true, message: t('batch.admin.fieldRequired', { field: f.label || f.key }) }]}
            >
              {f.secret ? <Input.Password placeholder={f.key} /> : <Input placeholder={f.key} />}
            </Form.Item>
          ))}
        </Form>
      </Modal>
    </Space>
  )
}
