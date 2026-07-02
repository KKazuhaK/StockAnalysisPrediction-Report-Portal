import { useEffect, useState } from 'react'
import { App, AutoComplete, Button, Checkbox, Form, Input, Modal, Popconfirm, Space, Table, Tag, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined, SaveOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { TypeGroup, TypeRow, TypesResp } from '../../api/types'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'

export default function TypesPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [groups, setGroups] = useState<TypeGroup[]>([])
  const [kinds, setKinds] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState<string[]>([])
  const [addForm] = Form.useForm()

  const load = () =>
    api
      .get<TypesResp>('/api/admin/types')
      .then((r) => {
        setGroups(r.groups || [])
        setKinds(r.kinds || [])
      })
      .finally(() => setLoading(false))

  useEffect(() => {
    load()
  }, [])

  const update = (name: string, patch: Partial<TypeRow>) =>
    setGroups((gs) => gs.map((g) => ({ ...g, rows: g.rows.map((r) => (r.name === name ? { ...r, ...patch } : r)) })))

  const reorderGroup = async (kind: string, orderedNames: string[]) => {
    setGroups((gs) =>
      gs.map((g) =>
        g.kind === kind ? { ...g, rows: orderedNames.map((n) => g.rows.find((r) => r.name === n)!) } : g,
      ),
    )
    await api.post('/api/admin/types/reorder', { names: orderedNames })
  }

  const remove = async (name: string) => {
    await api.del(`/api/admin/types/${encodeURIComponent(name)}`)
    load()
  }

  const save = async () => {
    setSaving(true)
    try {
      const rows = groups
        .flatMap((g) => g.rows)
        .map((r) => ({ name: r.name, label: r.label, kind: r.kind, summary: r.isSummary }))
      await api.post('/api/admin/types/save', { rows })
      message.success(t('common.saved'))
      load()
    } finally {
      setSaving(false)
    }
  }

  const add = async () => {
    const v = await addForm.validateFields()
    await api.post('/api/admin/types/add', { ...v, summary: !!v.summary })
    setOpen(false)
    message.success(t('common.done'))
    load()
  }

  const openAdd = () => {
    addForm.resetFields()
    setOpen(true)
  }

  const removeSelected = async () => {
    await Promise.all(selected.map((name) => api.del(`/api/admin/types/${encodeURIComponent(name)}`)))
    setSelected([])
    message.success(t('common.done'))
    load()
  }

  const kindOptions = kinds.map((k) => ({ value: k, label: k }))

  const columns = [
    { key: 'sort', width: 44, align: 'center' as const, render: () => <DragHandle /> },
    {
      title: t('common.name'),
      dataIndex: 'name',
      render: (n: string) => <Typography.Text strong>{n}</Typography.Text>,
    },
    {
      title: t('types.label'),
      dataIndex: 'label',
      render: (_: any, r: TypeRow) => (
        <Input
          size="small"
          value={r.label}
          placeholder={r.name}
          onChange={(e) => update(r.name, { label: e.target.value })}
        />
      ),
    },
    {
      title: t('types.kind'),
      dataIndex: 'kind',
      width: 150,
      render: (_: any, r: TypeRow) => (
        <AutoComplete
          size="small"
          value={r.kind}
          options={kindOptions}
          style={{ width: '100%' }}
          onChange={(v) => update(r.name, { kind: v })}
          filterOption={(input, opt) => String(opt?.value ?? '').toLowerCase().includes(input.toLowerCase())}
        />
      ),
    },
    {
      title: t('types.summary'),
      dataIndex: 'isSummary',
      width: 80,
      align: 'center' as const,
      render: (_: any, r: TypeRow) => (
        <Checkbox checked={r.isSummary} onChange={(e) => update(r.name, { isSummary: e.target.checked })} />
      ),
    },
    {
      title: '',
      width: 60,
      align: 'right' as const,
      render: (_: any, r: TypeRow) => (
        <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(r.name)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      ),
    },
  ]

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

      {groups.map((g) => {
        const groupNames = g.rows.map((r) => r.name)
        return (
          <div key={g.kind}>
            <Tag color="blue" style={{ marginBottom: 8, fontSize: 13 }}>
              {g.kind}
            </Tag>
            <SortableWrapper ids={groupNames} onReorder={(names) => reorderGroup(g.kind, names)}>
              <Table<TypeRow>
                rowKey="name"
                size="small"
                loading={loading}
                dataSource={g.rows}
                pagination={false}
                components={sortableTableComponents}
                rowSelection={{
                  selectedRowKeys: selected.filter((n) => groupNames.includes(n)),
                  onChange: (keys) =>
                    setSelected((prev) => [
                      ...prev.filter((n) => !groupNames.includes(n)),
                      ...(keys as string[]),
                    ]),
                }}
                columns={columns}
              />
            </SortableWrapper>
          </div>
        )
      })}

      <div>
        <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={save}>
          {t('types.save')}
        </Button>
      </div>

      <Modal
        open={open}
        title={t('common.add')}
        onOk={add}
        onCancel={() => setOpen(false)}
        okText={t('common.add')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={addForm} layout="vertical">
          <Form.Item name="name" label={t('types.addName')} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="kind" label={t('types.kind')}>
            <AutoComplete
              options={kindOptions}
              allowClear
              filterOption={(input, opt) => String(opt?.value ?? '').toLowerCase().includes(input.toLowerCase())}
            />
          </Form.Item>
          <Form.Item name="label" label={t('types.label')}>
            <Input />
          </Form.Item>
          <Form.Item name="summary" valuePropName="checked">
            <Checkbox>{t('types.summary')}</Checkbox>
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
