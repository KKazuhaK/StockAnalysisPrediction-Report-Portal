import { useEffect, useState } from 'react'
import {
  App,
  AutoComplete,
  Button,
  Checkbox,
  Form,
  Input,
  Modal,
  Popconfirm,
  Popover,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import { DeleteOutlined, DownOutlined, PlusOutlined, ReloadOutlined, RollbackOutlined, SaveOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { TypeGroup, TypeRow, TypesResp } from '../../api/types'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'
import StickyActionBar from '../../components/StickyActionBar'

// antd's Tag preset colors (https://ant.design/components/tag) — "default" maps
// to no color prop (the neutral grey Tag).
const TAG_COLORS = [
  'default',
  'magenta',
  'red',
  'volcano',
  'orange',
  'gold',
  'lime',
  'green',
  'cyan',
  'blue',
  'geekblue',
  'purple',
]

// Representative hex for each preset (Tag itself only exposes the named token,
// not a hex, and we need a solid fill for the swatch dots below).
const SWATCH_HEX: Record<string, string> = {
  magenta: '#eb2f96',
  red: '#f5222d',
  volcano: '#fa541c',
  orange: '#fa8c16',
  gold: '#faad14',
  lime: '#a0d911',
  green: '#52c41a',
  cyan: '#13c2c2',
  blue: '#1677ff',
  geekblue: '#2f54eb',
  purple: '#722ed1',
}

function ColorSwatch({ color, size, selected }: { color: string; size: number; selected?: boolean }) {
  const hex = SWATCH_HEX[color]
  return (
    <span
      style={{
        display: 'inline-block',
        width: size,
        height: size,
        borderRadius: '50%',
        flexShrink: 0,
        background: hex || 'var(--ant-color-fill-tertiary)',
        border: hex ? 'none' : '1px solid var(--ant-color-border)',
        boxShadow: selected
          ? `0 0 0 2px var(--ant-color-bg-container), 0 0 0 4px ${hex || 'var(--ant-color-text-tertiary)'}`
          : undefined,
      }}
    />
  )
}

// Compact swatch-grid color picker: a small dot button that opens a palette of
// dot buttons on click, replacing a plain <Select> of color-name Tags (which
// visually doubled up as nested pills-in-a-list).
function KindColorPicker({ color, onChange }: { color: string; onChange: (color: string) => void }) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  return (
    <Popover
      trigger="click"
      open={open}
      onOpenChange={setOpen}
      placement="bottomLeft"
      content={
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10, width: 168 }}>
          {TAG_COLORS.map((c) => (
            <Tooltip key={c} title={c}>
              <button
                type="button"
                aria-label={c}
                onClick={() => {
                  onChange(c)
                  setOpen(false)
                }}
                style={{ padding: 2, border: 'none', background: 'none', cursor: 'pointer', lineHeight: 0 }}
              >
                <ColorSwatch color={c} size={20} selected={c === color} />
              </button>
            </Tooltip>
          ))}
        </div>
      }
    >
      <button
        type="button"
        aria-label={t('types.kindColor')}
        style={{
          display: 'inline-flex',
          alignItems: 'center',
          gap: 5,
          padding: '3px 7px',
          border: '1px solid var(--ant-color-border)',
          borderRadius: 6,
          background: 'var(--ant-color-bg-container)',
          cursor: 'pointer',
        }}
      >
        <ColorSwatch color={color} size={14} />
        <DownOutlined style={{ fontSize: 9, color: 'var(--ant-color-text-tertiary)' }} />
      </button>
    </Popover>
  )
}

export default function TypesPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [groups, setGroups] = useState<TypeGroup[]>([])
  const [kinds, setKinds] = useState<string[]>([])
  const [colors, setColors] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState<string[]>([])
  const [reclassifying, setReclassifying] = useState(false)
  const [restoring, setRestoring] = useState(false)
  const [addForm] = Form.useForm()

  const load = () =>
    api
      .get<TypesResp>('/api/admin/types')
      .then((r) => {
        setGroups(r.groups || [])
        setKinds(r.kinds || [])
        setColors(r.colors || {})
      })
      .finally(() => setLoading(false))

  const saveColor = async (kind: string, color: string) => {
    setColors((c) => ({ ...c, [kind]: color }))
    await api.post('/api/admin/kind-colors', { kind, color })
  }

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

  // Re-apply the current subtype→大类 mapping to every stored report.
  const reclassify = async () => {
    setReclassifying(true)
    try {
      const r = await api.post<{ updated: number }>('/api/admin/types/recompute', {})
      message.success(t('types.reclassifyDone', { n: r.updated }))
      load()
    } finally {
      setReclassifying(false)
    }
  }

  // Wipe this page's type config and re-seed the shipped first-run defaults.
  // Custom types are removed; stored report data is untouched.
  const restoreDefaults = async () => {
    setRestoring(true)
    try {
      const r = await api.post<{ restored: number }>('/api/admin/types/restore-defaults', {})
      message.success(t('types.restoreDefaultsDone', { n: r.restored }))
      load()
    } finally {
      setRestoring(false)
    }
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
    // A plain flex column (not antd Space) so the sticky save bar's parent is a tall block it can
    // pin against — Space wraps each child in an item box only as tall as the bar itself.
    <div style={{ display: 'flex', flexDirection: 'column', gap: 16, width: '100%' }}>
      <Space wrap>
        <Button type="primary" icon={<PlusOutlined />} onClick={openAdd}>
          {t('common.add')}
        </Button>
        <Popconfirm title={t('types.reclassifyConfirm')} onConfirm={reclassify}>
          <Button icon={<ReloadOutlined />} loading={reclassifying}>
            {t('types.reclassify')}
          </Button>
        </Popconfirm>
        <Popconfirm title={t('types.restoreDefaultsConfirm')} onConfirm={restoreDefaults}>
          <Button icon={<RollbackOutlined />} loading={restoring}>
            {t('types.restoreDefaults')}
          </Button>
        </Popconfirm>
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
            <Space align="center" style={{ marginBottom: 8 }}>
              <Tag color={colors[g.kind] || 'default'} style={{ fontSize: 13 }}>
                {g.kind}
              </Tag>
              <KindColorPicker color={colors[g.kind] || 'default'} onChange={(color) => saveColor(g.kind, color)} />
            </Space>
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

      <StickyActionBar>
        <Button type="primary" icon={<SaveOutlined />} loading={saving} onClick={save}>
          {t('types.save')}
        </Button>
      </StickyActionBar>

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
    </div>
  )
}
