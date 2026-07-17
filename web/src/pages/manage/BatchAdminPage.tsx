import { useEffect, useMemo, useState } from 'react'
import { Alert, App, Button, Checkbox, Form, Input, Modal, Popconfirm, Space, Table, Tabs, Tag, Tooltip, Typography, Upload } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { ApiOutlined, DeleteOutlined, EditOutlined, EyeOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { difyModeTag, surfaceSupportsMode } from '../../lib/batchUi'
import { ALL_SURFACES } from '../../api/types'
import type { Surface } from '../../api/types'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'
import type { BatchPlugin, BatchTarget, DifyInput, DifyTargetEdit } from '../../api/types'

export default function BatchAdminPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()

  const [plugins, setPlugins] = useState<BatchPlugin[]>([])
  const [targets, setTargets] = useState<BatchTarget[]>([])

  const [targetOpen, setTargetOpen] = useState(false)
  // null = creating a new target; a number = editing that target's id.
  const [editingId, setEditingId] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [form] = Form.useForm()
  // Dify probe state
  const [probing, setProbing] = useState(false)
  const [probed, setProbed] = useState<{ name: string; inputsError?: string } | null>(null)
  const [inputs, setInputs] = useState<DifyInput[]>([])
  const [mode, setMode] = useState('') // Dify app mode: "" / "workflow" / "chat"
  const [newVar, setNewVar] = useState('')

  const isChat = mode !== '' && mode !== 'workflow'

  const editing = editingId !== null
  // The name + inputs section shows after a probe (create) or immediately (edit).
  const showDetails = editing || !!probed

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

  const resetModal = () => {
    form.resetFields()
    setProbed(null)
    setInputs([])
    setMode('')
    setNewVar('')
  }

  const openCreate = () => {
    resetModal()
    setEditingId(null)
    setTargetOpen(true)
  }

  // Load a target's editable config (name, base_url, inputs — never the api_key) and
  // open the modal in edit mode.
  const openEdit = async (tg: BatchTarget) => {
    try {
      const d = await api.get<DifyTargetEdit>(`/api/admin/batch/dify/targets/${tg.id}`)
      resetModal()
      form.setFieldsValue({ name: d.name, base_url: d.base_url, api_key: '' })
      setInputs(d.inputs || [])
      setMode(d.mode || '')
      setEditingId(tg.id)
      setTargetOpen(true)
    } catch (e) {
      message.error((e as Error).message)
    }
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
        // When editing, pass the target id so a blank key reuses the stored one.
        { base_url: v.base_url, api_key: v.api_key || '', target_id: editing ? editingId : undefined },
      )
      setProbed({ name: r.name, inputsError: r.inputs_error })
      setInputs(r.inputs || [])
      setMode(r.mode || '')
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
    // api_key is always read back: in create mode its rule makes it required; in edit mode
    // the rule is optional, so a blank keeps the stored key while a typed value rotates it.
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
    setSaving(true)
    try {
      const body = { name: v.name, base_url: v.base_url, api_key: v.api_key || '', mode, inputs }
      if (editing) {
        await api.put(`/api/admin/batch/dify/targets/${editingId}`, body)
        message.success(t('batch.admin.msgTargetUpdated'))
      } else {
        await api.post('/api/admin/batch/dify/targets', body)
        message.success(t('batch.admin.msgTargetCreated'))
      }
      setTargetOpen(false)
      loadTargets()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSaving(false)
    }
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
  // Visibility editing. Kept out of the Dify edit modal on purpose: that one edits the
  // workflow CONNECTION (base_url/api_key) and only exists for dify targets, while
  // visibility is portal policy that applies to every target kind.
  const [surfaceFor, setSurfaceFor] = useState<BatchTarget | null>(null)
  const [surfaceSel, setSurfaceSel] = useState<Surface[]>([])

  const openSurfaces = (tg: BatchTarget) => {
    setSurfaceSel(tg.surfaces && tg.surfaces.length ? tg.surfaces : ALL_SURFACES)
    setSurfaceFor(tg)
  }
  const saveSurfaces = async () => {
    if (!surfaceFor) return
    if (surfaceSel.length === 0) {
      message.warning(t('batch.admin.surfacesEmpty'))
      return
    }
    await api.put(`/api/admin/batch/targets/${surfaceFor.id}/surfaces`, { surfaces: surfaceSel })
    setSurfaceFor(null)
    loadTargets()
  }

  const deleteTarget = async (id: number) => {
    await api.del(`/api/admin/batch/targets/${id}`)
    loadTargets()
  }

  // Persist the admin's drag order; optimistically reflect it, then save (same pattern as
  // links / report types).
  const reorderTargets = async (orderedIds: string[]) => {
    const byId = new Map(targets.map((tg) => [String(tg.id), tg]))
    setTargets(orderedIds.map((id) => byId.get(id)!).filter(Boolean))
    await api.post('/api/admin/batch/targets/reorder', { ids: orderedIds.map(Number) })
    loadTargets()
  }

  const targetCols: ColumnsType<BatchTarget> = [
    { key: 'sort', width: 44, align: 'center', render: () => <DragHandle /> },
    {
      title: t('common.name'),
      render: (_: unknown, tg: BatchTarget) => (
        <Space size={6}>
          {tg.name}
          {difyModeTag(t, tg.mode)}
        </Space>
      ),
    },
    { title: t('batch.admin.inputs'), render: (_: unknown, tg: BatchTarget) => (tg.inputs || []).map((i) => <Tag key={i.key}>{i.key}</Tag>) },
    {
      title: t('batch.admin.surfaces'),
      width: 230,
      render: (_: unknown, tg: BatchTarget) => {
        const list = tg.surfaces && tg.surfaces.length ? tg.surfaces : ALL_SURFACES
        // Only the surfaces this app kind can actually serve are worth showing: an agent
        // app tagged "运行分析" would be a promise the capability rule breaks.
        const usable = list.filter((sf) => surfaceSupportsMode(sf, tg.mode))
        if (usable.length === ALL_SURFACES.filter((sf) => surfaceSupportsMode(sf, tg.mode)).length) {
          return <Tag>{t('batch.surface.all')}</Tag>
        }
        return (
          <Space size={4} wrap>
            {usable.map((sf) => (
              <Tag key={sf} color="blue">
                {t(`batch.surface.${sf}`)}
              </Tag>
            ))}
          </Space>
        )
      },
    },
    { title: t('batch.admin.createdAt'), dataIndex: 'created_at', width: 170 },
    {
      title: t('batch.col.actions'),
      width: 132,
      render: (_: unknown, tg: BatchTarget) => (
        <Space size={4}>
          <Button size="small" icon={<EyeOutlined />} title={t('batch.admin.surfacesTitle')} onClick={() => openSurfaces(tg)} />
          {tg.plugin_slug === 'dify' && (
            <Button size="small" icon={<EditOutlined />} title={t('common.edit')} onClick={() => openEdit(tg)} />
          )}
          <Popconfirm title={t('batch.admin.deleteTargetConfirm')} onConfirm={() => deleteTarget(tg.id)}>
            <Button size="small" danger icon={<DeleteOutlined />} title={t('common.delete')} />
          </Popconfirm>
        </Space>
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
    <>
      <Modal
        title={surfaceFor ? `${t('batch.admin.surfacesTitle')} — ${surfaceFor.name}` : t('batch.admin.surfacesTitle')}
        open={!!surfaceFor}
        onCancel={() => setSurfaceFor(null)}
        onOk={saveSurfaces}
        okButtonProps={{ disabled: surfaceSel.length === 0 }}
        width={460}
        destroyOnHidden
      >
        <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
          {t('batch.admin.surfacesHint')}
        </Typography.Paragraph>
        <Space orientation="vertical" size={14} style={{ width: '100%' }}>
          {ALL_SURFACES.map((sf) => {
            // A surface the app kind cannot serve is disabled, not merely unhelpful: an
            // admin who ticks it would otherwise save a box that silently does nothing,
            // because capability is ANDed with policy at read time.
            const supported = surfaceSupportsMode(sf, surfaceFor?.mode)
            const box = (
              <Checkbox
                key={sf}
                disabled={!supported}
                checked={supported && surfaceSel.includes(sf)}
                onChange={(e) =>
                  setSurfaceSel((prev) => (e.target.checked ? [...prev, sf] : prev.filter((x) => x !== sf)))
                }
              >
                {t(`batch.surface.${sf}`)}
              </Checkbox>
            )
            if (supported) return <div key={sf}>{box}</div>
            const why = sf === 'chat' ? 'batch.admin.surfacesWorkflowHint' : 'batch.admin.surfacesAgentHint'
            return (
              <Tooltip key={sf} title={t(why)}>
                <div>{box}</div>
              </Tooltip>
            )
          })}
        </Space>
      </Modal>
      <Tabs
        defaultActiveKey="targets"
        items={[
          {
            key: 'targets',
            label: t('batch.admin.targets'),
            children: (
              <Space direction="vertical" size={12} style={{ width: '100%' }}>
                <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                  <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
                    {t('batch.dify.newTarget')}
                  </Button>
                </div>
                {targets.length === 0 && <Typography.Text type="secondary">{t('batch.dify.targetsHint')}</Typography.Text>}
                <SortableWrapper ids={targets.map((tg) => String(tg.id))} onReorder={reorderTargets}>
                  <Table
                    rowKey="id"
                    size="small"
                    dataSource={targets}
                    columns={targetCols}
                    pagination={false}
                    components={sortableTableComponents}
                    scroll={{ x: 'max-content' }}
                  />
                </SortableWrapper>
              </Space>
            ),
          },
          {
            key: 'plugins',
            label: t('batch.admin.advancedPlugins'),
            children: (
              <Space direction="vertical" size={12} style={{ width: '100%' }}>
                <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
                  <Upload accept=".json" showUploadList={false} beforeUpload={importFile}>
                    <Button icon={<UploadOutlined />}>{t('batch.admin.importManifest')}</Button>
                  </Upload>
                </div>
                {customPlugins.length === 0 ? (
                  <Typography.Text type="secondary">{t('batch.admin.advancedPluginsHint')}</Typography.Text>
                ) : (
                  <Table rowKey="slug" size="small" dataSource={customPlugins} columns={pluginCols} pagination={false} scroll={{ x: 'max-content' }} />
                )}
              </Space>
            ),
          },
        ]}
      />

      {/* Create / edit a Dify workflow target */}
      <Modal
        title={editing ? t('batch.dify.editTarget') : t('batch.dify.newTarget')}
        open={targetOpen}
        onOk={saveTarget}
        confirmLoading={saving}
        okButtonProps={{ disabled: !editing && !probed }}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        onCancel={() => setTargetOpen(false)}
        forceRender
      >
        <Form form={form} layout="vertical">
          <Form.Item name="base_url" label={t('batch.dify.baseUrl')} extra={t('batch.dify.baseUrlHint')} rules={[{ required: true }]}>
            <Input placeholder="https://dify.example.com/v1" />
          </Form.Item>
          <Form.Item
            name="api_key"
            label={t('batch.dify.apiKey')}
            extra={editing ? t('batch.dify.apiKeyKeepHint') : t('batch.dify.apiKeyHint')}
            rules={editing ? [] : [{ required: true }]}
          >
            <Input.Password placeholder={editing ? t('batch.dify.apiKeyKeepPlaceholder') : 'app-…'} autoComplete="new-password" />
          </Form.Item>
          <Button icon={<ApiOutlined />} loading={probing} onClick={probe}>
            {editing ? t('batch.dify.reprobe') : t('batch.dify.probe')}
          </Button>

          {showDetails && (
            <div style={{ marginTop: 14 }}>
              {probed && (
                <Alert
                  type={probed.inputsError ? 'warning' : 'success'}
                  showIcon
                  message={probed.inputsError ? t('batch.dify.connectedNoInputs', { name: probed.name }) : t('batch.dify.connected', { name: probed.name })}
                />
              )}
              <Form.Item name="name" label={t('batch.admin.targetName')} rules={[{ required: true }]} style={{ marginTop: 14 }}>
                <Input placeholder={t('batch.admin.targetNamePlaceholder')} />
              </Form.Item>
              {isChat && (
                <Alert type="info" showIcon style={{ marginBottom: 10 }} message={<>{difyModeTag(t, mode)}{t('batch.dify.chatHint')}</>} />
              )}
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
    </>
  )
}
