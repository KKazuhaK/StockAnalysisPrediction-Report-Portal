import { useEffect, useState } from 'react'
import { App, Button, Checkbox, Form, Input, Modal, Popconfirm, Radio, Select, Space, Table, Tag, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { useAuth } from '../../auth'
import type { AppSummary, AppsResp, BatchTarget, ChatTarget, LinkItem } from '../../api/types'
import { difyModeKind } from '../../lib/batchUi'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'
import { LINK_ICON_OPTIONS, linkIconComponent } from '../../components/linkIcons'
import { APP_SHORTCUTS, shortcutOfUrl, shortcutUrl } from '../../lib/shortcuts'

// Options for the icon picker: each renders its glyph + name.
const iconSelectOptions = LINK_ICON_OPTIONS.map(({ value }) => {
  const Icon = linkIconComponent(value)
  return {
    value,
    label: (
      <Space size={8}>
        <Icon />
        {value}
      </Space>
    ),
  }
})

export default function LinksPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { can } = useAuth()
  const canRun = can('run_batch')
  const [links, setLinks] = useState<LinkItem[]>([])
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<LinkItem | null>(null)
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm()
  // Target lists so a shortcut can be pinned to a specific workflow / assistant / app.
  const [batchTargets, setBatchTargets] = useState<BatchTarget[]>([])
  const [chatTargets, setChatTargets] = useState<ChatTarget[]>([])
  const [appList, setAppList] = useState<AppSummary[]>([])

  const load = () =>
    api
      .get<{ links: LinkItem[] }>('/api/admin/links')
      .then((r) => setLinks(r.links || []))
      .finally(() => setLoading(false))

  useEffect(() => {
    load()
  }, [])

  // Populate the pinned-target picker. batch/chat targets need run_batch (admins usually have
  // it); apps are readable by any user. Failures are non-fatal — the picker just stays empty.
  useEffect(() => {
    api.get<AppsResp>('/api/apps').then((r) => setAppList(r.apps || [])).catch(() => {})
    if (canRun) {
      api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setBatchTargets(r.targets || [])).catch(() => {})
      api.get<{ targets: ChatTarget[] }>('/api/chat/targets').then((r) => setChatTargets(r.targets || [])).catch(() => {})
    }
  }, [canRun])

  // The specific-target options for a given shortcut key. run-analysis matches the run modal's
  // filter (non-agent); chat lists assistants; apps lists installed apps (string ids).
  const targetOptionsFor = (key?: string): { value: string; label: string }[] => {
    if (key === 'run-analysis') return batchTargets.filter((tg) => difyModeKind(tg.mode) !== 'agent').map((tg) => ({ value: String(tg.id), label: tg.name }))
    if (key === 'chat') return chatTargets.map((tg) => ({ value: String(tg.id), label: tg.name }))
    if (key === 'apps') return appList.map((a) => ({ value: a.id, label: a.name }))
    return []
  }

  const openAdd = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ kind: 'url', newTab: true, collapsed: false }) // default: an external link, new tab, shown inline
    setOpen(true)
  }
  const openEdit = (l: LinkItem) => {
    setEditing(l)
    const res = shortcutOfUrl(l.url)
    form.setFieldsValue({
      label: l.label,
      icon: l.icon,
      kind: res ? 'shortcut' : 'url',
      shortcut: res?.shortcut.key,
      shortcutTarget: res?.param,
      url: res ? '' : l.url,
      newTab: l.newTab !== false,
      collapsed: !!l.collapsed,
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    // A shortcut is stored as url = "rp:<key>[:<target>]"; a plain link keeps its URL + new-tab flag.
    const payload =
      v.kind === 'shortcut'
        ? { label: v.label, url: shortcutUrl(v.shortcut, v.shortcutTarget), icon: v.icon, newTab: false, collapsed: !!v.collapsed }
        : { label: v.label, url: v.url, icon: v.icon, newTab: v.newTab, collapsed: !!v.collapsed }
    if (editing) await api.put(`/api/admin/links/${editing.id}`, payload)
    else await api.post('/api/admin/links', payload)
    setOpen(false)
    message.success(t('common.saved'))
    load()
  }

  // When a target is picked and the button has no name yet, offer the target's name as a
  // convenience — always editable, never overwriting a name the admin already typed.
  const onValuesChange = (changed: Record<string, unknown>) => {
    if ('shortcutTarget' in changed && changed.shortcutTarget && !form.getFieldValue('label')) {
      const name = targetOptionsFor(form.getFieldValue('shortcut')).find((o) => o.value === changed.shortcutTarget)?.label
      if (name) form.setFieldValue('label', name)
    }
  }

  const remove = async (id: number) => {
    await api.del(`/api/admin/links/${id}`)
    load()
  }

  const reorder = async (orderedIds: string[]) => {
    setLinks((prev) => orderedIds.map((id) => prev.find((l) => String(l.id) === id)!).filter(Boolean))
    await api.post('/api/admin/links/reorder', { ids: orderedIds.map(Number) })
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }}>
        <Typography.Text type="secondary">{t('links.hint')}</Typography.Text>
        <Button type="primary" icon={<PlusOutlined />} onClick={openAdd}>
          {t('common.add')}
        </Button>
      </Space>

      <SortableWrapper ids={links.map((l) => String(l.id))} onReorder={reorder}>
        <Table<LinkItem>
          rowKey={(r) => String(r.id)}
          loading={loading}
          dataSource={links}
          pagination={false}
          components={sortableTableComponents}
          columns={[
            { key: 'sort', width: 48, align: 'center', render: () => <DragHandle /> },
            {
              title: t('links.icon'),
              dataIndex: 'icon',
              width: 60,
              align: 'center',
              render: (icon: string) => {
                const Icon = linkIconComponent(icon)
                return <Icon />
              },
            },
            { title: t('links.label'), dataIndex: 'label' },
            {
              title: t('links.url'),
              dataIndex: 'url',
              render: (u: string) => {
                const res = shortcutOfUrl(u)
                if (!res) {
                  return (
                    <a href={u} target="_blank" rel="noreferrer">
                      {u}
                    </a>
                  )
                }
                // Resolve a pinned target to its name (falling back to #id if the list is
                // unloaded or the target was removed).
                const suffix = res.param ? ` · ${targetOptionsFor(res.shortcut.key).find((o) => o.value === res.param)?.label ?? '#' + res.param}` : ''
                return (
                  <Tag color="blue">
                    {t('links.shortcutTag', { name: t(res.shortcut.labelKey) })}
                    {suffix}
                  </Tag>
                )
              },
            },
            {
              title: t('links.newTab'),
              dataIndex: 'newTab',
              width: 96,
              align: 'center',
              render: (v: boolean) => <Checkbox checked={v !== false} disabled />,
            },
            {
              title: t('links.collapsed'),
              dataIndex: 'collapsed',
              width: 96,
              align: 'center',
              render: (v: boolean) => <Checkbox checked={!!v} disabled />,
            },
            {
              title: '',
              width: 120,
              align: 'right',
              render: (_, l) => (
                <Space>
                  <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(l)} />
                  <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(l.id)}>
                    <Button size="small" danger icon={<DeleteOutlined />} />
                  </Popconfirm>
                </Space>
              ),
            },
          ]}
        />
      </SortableWrapper>

      <Modal
        open={open}
        title={editing ? t('common.edit') : t('common.add')}
        onOk={submit}
        onCancel={() => setOpen(false)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={form} layout="vertical" onValuesChange={onValuesChange}>
          <Form.Item name="label" label={t('links.label')} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="kind" label={t('links.type')} initialValue="url">
            <Radio.Group optionType="button" buttonStyle="solid">
              <Radio.Button value="url">{t('links.typeUrl')}</Radio.Button>
              <Radio.Button value="shortcut">{t('links.typeShortcut')}</Radio.Button>
            </Radio.Group>
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.kind !== b.kind || a.shortcut !== b.shortcut}>
            {({ getFieldValue }) => {
              if (getFieldValue('kind') !== 'shortcut') {
                return (
                  <Form.Item name="url" label={t('links.url')} rules={[{ required: true }]}>
                    <Input placeholder="https://…" />
                  </Form.Item>
                )
              }
              const key = getFieldValue('shortcut') as string | undefined
              const sc = APP_SHORTCUTS.find((s) => s.key === key)
              return (
                <>
                  <Form.Item name="shortcut" label={t('links.shortcut')} rules={[{ required: true }]}>
                    <Select
                      placeholder={t('links.shortcutPlaceholder')}
                      options={APP_SHORTCUTS.map((s) => ({ value: s.key, label: t(s.labelKey) }))}
                    />
                  </Form.Item>
                  {sc?.hasTarget && (
                    <Form.Item name="shortcutTarget" label={t('links.shortcutTarget')}>
                      <Select
                        allowClear
                        showSearch
                        optionFilterProp="label"
                        placeholder={t('links.shortcutTargetPlaceholder')}
                        options={targetOptionsFor(key)}
                      />
                    </Form.Item>
                  )}
                </>
              )
            }}
          </Form.Item>
          <Form.Item name="icon" label={t('links.icon')}>
            <Select allowClear showSearch placeholder={t('links.iconPlaceholder')} options={iconSelectOptions} optionFilterProp="value" />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.kind !== b.kind}>
            {({ getFieldValue }) =>
              getFieldValue('kind') === 'shortcut' ? null : (
                <Form.Item name="newTab" valuePropName="checked">
                  <Checkbox>{t('links.newTab')}</Checkbox>
                </Form.Item>
              )
            }
          </Form.Item>
          <Form.Item name="collapsed" valuePropName="checked">
            <Checkbox>{t('links.collapsed')}</Checkbox>
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
