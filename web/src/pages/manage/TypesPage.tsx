import { useEffect, useState } from 'react'
import { App, AutoComplete, Button, Checkbox, Divider, Form, Input, Popconfirm, Space, Table, Tag, Typography } from 'antd'
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
    addForm.resetFields()
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
      {/* Add new type */}
      <Form form={addForm} layout="inline" onFinish={add}>
        <Form.Item name="name" rules={[{ required: true }]}>
          <Input placeholder={t('types.addName')} style={{ width: 160 }} />
        </Form.Item>
        <Form.Item name="kind">
          <AutoComplete
            placeholder={t('types.kind')}
            options={kindOptions}
            style={{ width: 140 }}
            allowClear
            filterOption={(input, opt) => String(opt?.value ?? '').toLowerCase().includes(input.toLowerCase())}
          />
        </Form.Item>
        <Form.Item name="label">
          <Input placeholder={t('types.label')} style={{ width: 160 }} />
        </Form.Item>
        <Form.Item name="summary" valuePropName="checked">
          <Checkbox>{t('types.summary')}</Checkbox>
        </Form.Item>
        <Form.Item>
          <Button icon={<PlusOutlined />} htmlType="submit">
            {t('common.add')}
          </Button>
        </Form.Item>
      </Form>

      <Divider style={{ margin: '4px 0' }} />

      {groups.map((g) => (
        <div key={g.kind}>
          <Tag color="blue" style={{ marginBottom: 8, fontSize: 13 }}>
            {g.kind}
          </Tag>
          <SortableWrapper ids={g.rows.map((r) => r.name)} onReorder={(names) => reorderGroup(g.kind, names)}>
            <Table<TypeRow>
              rowKey="name"
              size="small"
              loading={loading}
              dataSource={g.rows}
              pagination={false}
              components={sortableTableComponents}
              columns={columns}
            />
          </SortableWrapper>
        </div>
      ))}

      <div>
        <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={save}>
          {t('types.save')}
        </Button>
      </div>
    </Space>
  )
}
