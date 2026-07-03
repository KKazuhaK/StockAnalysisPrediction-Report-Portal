import { useEffect, useMemo, useState } from 'react'
import { Alert, App, Button, Card, Form, Input, Modal, Popconfirm, Space, Table, Tag, Typography, Upload } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { ApiOutlined, DeleteOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { BatchPlugin, BatchTarget, DifyInput } from '../../api/types'

export default function BatchAdminPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()

  const [plugins, setPlugins] = useState<BatchPlugin[]>([])
  const [targets, setTargets] = useState<BatchTarget[]>([])

  const [targetOpen, setTargetOpen] = useState(false)
  const [form] = Form.useForm()
  // Dify probe state
  const [probing, setProbing] = useState(false)
  const [probed, setProbed] = useState<{ name: string; inputsError?: string } | null>(null)
  const [inputs, setInputs] = useState<DifyInput[]>([])
  const [newVar, setNewVar] = useState('')

  const loadPlugins = () =>
    api.get<{ plugins: BatchPlugin[] }>('/api/admin/batch/plugins').then((r) => setPlugins(r.plugins || []))
  const loadTargets = () =>
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || []))

  useEffect(() => {
    loadPlugins()
    loadTargets()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Custom (non-Dify) plugins — the bundled "dify" adapter is hidden; it's implied.
  const customPlugins = useMemo(() => plugins.filter((p) => p.slug !== 'dify'), [plugins])

  const openTarget = () => {
    form.resetFields()
    setProbed(null)
    setInputs([])
    setNewVar('')
    setTargetOpen(true)
  }

  // Probe: ask Dify for the workflow's name + input fields from the pasted key.
  const probe = async () => {
    let v
    try {
      v = await form.validateFields(['base_url', 'api_key'])
    } catch {
      return
    }
    setProbing(true)
    try {
      const r = await api.post<{ name: string; mode?: string; inputs: DifyInput[]; inputs_error?: string }>(
        '/api/admin/batch/dify/probe',
        { base_url: v.base_url, api_key: v.api_key },
      )
      setProbed({ name: r.name, inputsError: r.inputs_error })
      setInputs(r.inputs || [])
      if (!form.getFieldValue('name') && r.name) form.setFieldValue('name', r.name)
      if (r.inputs_error) message.warning(t('batch.dify.inputsManual'))
      else message.success(t('batch.dify.probed', { name: r.name }))
    } catch (e) {
      setProbed(null)
      message.error(t('batch.dify.probeFailed', { error: (e as Error).message }))
    } finally {
      setProbing(false)
    }
  }

  const addInput = () => {
    const v = newVar.trim()
    if (v && !inputs.some((i) => i.variable === v)) setInputs([...inputs, { variable: v, required: false }])
    setNewVar('')
  }

  const saveTarget = async () => {
    let v
    try {
      v = await form.validateFields(['name', 'base_url', 'api_key'])
    } catch {
      return
    }
    if (inputs.length === 0) {
      message.error(t('batch.dify.needInputs'))
      return
    }
    await api.post('/api/admin/batch/dify/targets', { name: v.name, base_url: v.base_url, api_key: v.api_key, inputs })
    setTargetOpen(false)
    message.success(t('batch.admin.msgTargetCreated'))
    loadTargets()
  }

  const importFile = (file: File) => {
    const reader = new FileReader()
    reader.onload = async () => {
      try {
        await api.post('/api/admin/batch/plugins/import', JSON.parse(String(reader.result)))
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
  const deleteTarget = async (id: number) => {
    await api.del(`/api/admin/batch/targets/${id}`)
    loadTargets()
  }

  const targetCols: ColumnsType<BatchTarget> = [
    { title: t('common.name'), dataIndex: 'name' },
    { title: t('batch.admin.inputs'), render: (_: unknown, tg: BatchTarget) => (tg.inputs || []).map((i) => <Tag key={i.key}>{i.key}</Tag>) },
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

  const pluginCols: ColumnsType<BatchPlugin> = [
    { title: t('common.name'), dataIndex: 'name' },
    { title: t('batch.admin.slug'), dataIndex: 'slug' },
    { title: t('batch.admin.version'), dataIndex: 'version', width: 90 },
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

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={t('batch.admin.targets')}
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openTarget}>
            {t('batch.dify.newTarget')}
          </Button>
        }
      >
        {targets.length === 0 && <Typography.Text type="secondary">{t('batch.dify.targetsHint')}</Typography.Text>}
        <Table rowKey="id" size="small" dataSource={targets} columns={targetCols} pagination={false} style={{ marginTop: targets.length ? 0 : 12 }} />
      </Card>

      {/* Advanced: custom (non-Dify) manifest plugins */}
      <Card
        title={<Typography.Text type="secondary">{t('batch.admin.advancedPlugins')}</Typography.Text>}
        extra={
          <Upload accept=".json" showUploadList={false} beforeUpload={importFile}>
            <Button size="small" icon={<UploadOutlined />}>
              {t('batch.admin.importManifest')}
            </Button>
          </Upload>
        }
      >
        {customPlugins.length === 0 ? (
          <Typography.Text type="secondary">{t('batch.admin.advancedPluginsHint')}</Typography.Text>
        ) : (
          <Table rowKey="slug" size="small" dataSource={customPlugins} columns={pluginCols} pagination={false} />
        )}
      </Card>

      {/* New Dify workflow target */}
      <Modal
        title={t('batch.dify.newTarget')}
        open={targetOpen}
        onOk={saveTarget}
        okButtonProps={{ disabled: !probed }}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        onCancel={() => setTargetOpen(false)}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="base_url" label={t('batch.dify.baseUrl')} extra={t('batch.dify.baseUrlHint')} rules={[{ required: true }]}>
            <Input placeholder="https://dify.example.com/v1" />
          </Form.Item>
          <Form.Item name="api_key" label={t('batch.dify.apiKey')} extra={t('batch.dify.apiKeyHint')} rules={[{ required: true }]}>
            <Input.Password placeholder="app-…" autoComplete="new-password" />
          </Form.Item>
          <Button icon={<ApiOutlined />} loading={probing} onClick={probe}>
            {t('batch.dify.probe')}
          </Button>

          {probed && (
            <div style={{ marginTop: 14 }}>
              <Alert
                type={probed.inputsError ? 'warning' : 'success'}
                showIcon
                message={probed.inputsError ? t('batch.dify.connectedNoInputs', { name: probed.name }) : t('batch.dify.connected', { name: probed.name })}
              />
              <Form.Item name="name" label={t('batch.admin.targetName')} rules={[{ required: true }]} style={{ marginTop: 14 }}>
                <Input placeholder={t('batch.admin.targetNamePlaceholder')} />
              </Form.Item>
              <div style={{ marginBottom: 6 }}>
                <Typography.Text type="secondary">{t('batch.dify.inputsLabel')}</Typography.Text>
              </div>
              <Space wrap size={[4, 4]} style={{ marginBottom: 8 }}>
                {inputs.map((i) => (
                  <Tag key={i.variable} closable onClose={() => setInputs(inputs.filter((x) => x.variable !== i.variable))} color={i.required ? 'blue' : undefined}>
                    {i.variable}
                    {i.required ? ' *' : ''}
                  </Tag>
                ))}
                {inputs.length === 0 && <Typography.Text type="secondary">{t('batch.dify.noInputs')}</Typography.Text>}
              </Space>
              <Space.Compact style={{ width: '100%' }}>
                <Input placeholder={t('batch.dify.addInputPlaceholder')} value={newVar} onChange={(e) => setNewVar(e.target.value)} onPressEnter={addInput} />
                <Button onClick={addInput}>{t('common.add')}</Button>
              </Space.Compact>
            </div>
          )}
        </Form>
      </Modal>
    </Space>
  )
}
