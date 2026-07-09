import { useEffect, useMemo, useState } from 'react'
import { App, Button, Checkbox, Empty, Form, Input, Modal, Popconfirm, Radio, Select, Space, Switch, Tag, Tooltip, Typography, theme } from 'antd'
import { DeleteOutlined, EditOutlined, FolderAddOutlined, FolderOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { DndContext, PointerSensor, closestCenter, useSensor, useSensors, type DragEndEvent } from '@dnd-kit/core'
import { restrictToParentElement, restrictToVerticalAxis } from '@dnd-kit/modifiers'
import { SortableContext, arrayMove, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { api } from '../../api/client'
import { useAuth } from '../../auth'
import type { AppSummary, AppsResp, BatchTarget, ChatTarget, LinkGroup, LinkGroupMode, LinkItem } from '../../api/types'
import { difyModeKind } from '../../lib/batchUi'
import { DragHandle, SortableItem } from './dnd'
import { LINK_ICON_OPTIONS, linkIconComponent } from '../../components/linkIcons'
import { APP_SHORTCUTS, shortcutOfUrl, shortcutUrl } from '../../lib/shortcuts'

const iconSelectOptions = LINK_ICON_OPTIONS.map(({ value }) => {
  const Icon = linkIconComponent(value)
  return { value, label: <Space size={8}><Icon />{value}</Space> }
})

// One row in the admin's single drag list: either a group header or an entry button.
type Row = { key: string; type: 'group'; group: LinkGroup } | { key: string; type: 'link'; link: LinkItem }

export default function LinksPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { token } = theme.useToken()
  const { can } = useAuth()
  const canRun = can('run_batch')
  const [links, setLinks] = useState<LinkItem[]>([])
  const [groups, setGroups] = useState<LinkGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<LinkItem | null>(null)
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm()
  const [groupEditing, setGroupEditing] = useState<LinkGroup | null>(null)
  const [groupOpen, setGroupOpen] = useState(false)
  const [groupForm] = Form.useForm()
  const [batchTargets, setBatchTargets] = useState<BatchTarget[]>([])
  const [chatTargets, setChatTargets] = useState<ChatTarget[]>([])
  const [appList, setAppList] = useState<AppSummary[]>([])
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }))

  const load = () =>
    api
      .get<{ links: LinkItem[]; groups: LinkGroup[] }>('/api/admin/links')
      .then((r) => {
        setLinks(r.links || [])
        setGroups(r.groups || [])
      })
      .finally(() => setLoading(false))

  useEffect(() => {
    load()
  }, [])

  useEffect(() => {
    api.get<AppsResp>('/api/apps').then((r) => setAppList(r.apps || [])).catch(() => {})
    if (canRun) {
      api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setBatchTargets(r.targets || [])).catch(() => {})
      api.get<{ targets: ChatTarget[] }>('/api/chat/targets').then((r) => setChatTargets(r.targets || [])).catch(() => {})
    }
  }, [canRun])

  const targetOptionsFor = (key?: string): { value: string; label: string }[] => {
    if (key === 'run-analysis') return batchTargets.filter((tg) => difyModeKind(tg.mode) !== 'agent').map((tg) => ({ value: String(tg.id), label: tg.name }))
    if (key === 'chat') return chatTargets.map((tg) => ({ value: String(tg.id), label: tg.name }))
    if (key === 'apps') return appList.map((a) => ({ value: a.id, label: a.name }))
    return []
  }

  const modeOptions: { value: LinkGroupMode; label: string }[] = [
    { value: 'row', label: t('links.mode.row') },
    { value: 'expand', label: t('links.mode.expand') },
    { value: 'popover', label: t('links.mode.popover') },
    { value: 'modal', label: t('links.mode.modal') },
  ]

  // The flat display list: ungrouped links first (top level), then each group header
  // followed by its own links. A link's group is whichever header sits above it.
  const rows = useMemo<Row[]>(() => {
    const linksIn = (gid: number) => links.filter((l) => (l.groupId || 0) === gid).sort((a, b) => a.ord - b.ord)
    const out: Row[] = []
    for (const l of linksIn(0)) out.push({ key: 'l:' + l.id, type: 'link', link: l })
    for (const g of [...groups].sort((a, b) => a.ord - b.ord)) {
      out.push({ key: 'g:' + g.id, type: 'group', group: g })
      for (const l of linksIn(g.id)) out.push({ key: 'l:' + l.id, type: 'link', link: l })
    }
    return out
  }, [links, groups])
  const ids = rows.map((r) => r.key)

  // Persist the whole layout after a drag: the backend re-derives each link's group + order
  // and each group's order from the item sequence.
  const persistLayout = (orderedIds: string[]) =>
    api.post('/api/admin/links/layout', { items: orderedIds.map((k) => ({ kind: k.startsWith('g:') ? 'group' : 'link', id: Number(k.slice(2)) })) }).catch(() => {})

  // Optimistically rebuild links (group + order) and groups (order) from a new sequence.
  const applyOrder = (orderedIds: string[]) => {
    const linkById = new Map(links.map((l) => [l.id, l]))
    const groupById = new Map(groups.map((g) => [g.id, g]))
    let current = 0
    const nextLinks: LinkItem[] = []
    const groupOrder: number[] = []
    orderedIds.forEach((key) => {
      const id = Number(key.slice(2))
      if (key.startsWith('g:')) {
        current = id
        groupOrder.push(id)
      } else {
        const l = linkById.get(id)
        if (l) nextLinks.push({ ...l, groupId: current, ord: nextLinks.length })
      }
    })
    setLinks(nextLinks)
    setGroups(groupOrder.map((id, i) => ({ ...(groupById.get(id) as LinkGroup), ord: i })))
  }

  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (!over || active.id === over.id) return
    const from = ids.indexOf(String(active.id))
    const to = ids.indexOf(String(over.id))
    if (from === -1 || to === -1) return
    let next = arrayMove(ids, from, to)
    // Moving a group header carries its links: pull them out and reinsert right after the
    // header, so the whole group travels as a block (links alone drag normally).
    if (String(active.id).startsWith('g:')) {
      const gid = Number(String(active.id).slice(2))
      const childKeys = links.filter((l) => (l.groupId || 0) === gid).map((l) => 'l:' + l.id)
      next = next.filter((k) => !childKeys.includes(k))
      const at = next.indexOf(String(active.id))
      next.splice(at + 1, 0, ...childKeys)
    }
    applyOrder(next)
    persistLayout(next)
  }

  // Groups are edited through the same button→modal flow as links (no more always-live inline
  // fields): "add group" opens a blank modal, the pencil on a group row opens it pre-filled.
  const openGroupAdd = () => {
    setGroupEditing(null)
    groupForm.resetFields()
    groupForm.setFieldsValue({ mode: 'expand', showLabel: true, icon: undefined })
    setGroupOpen(true)
  }
  const openGroupEdit = (g: LinkGroup) => {
    setGroupEditing(g)
    groupForm.setFieldsValue({ name: g.name, mode: g.mode, showLabel: g.showLabel, icon: g.icon || undefined })
    setGroupOpen(true)
  }
  const submitGroup = async () => {
    const v = await groupForm.validateFields()
    // Preserve the visibility toggle across an edit (the modal doesn't expose it).
    const payload = { name: (v.name || '').trim(), mode: v.mode, showLabel: !!v.showLabel, icon: v.icon || '', visible: groupEditing ? groupEditing.visible !== false : true }
    if (groupEditing) await api.put(`/api/admin/link-groups/${groupEditing.id}`, payload)
    else await api.post('/api/admin/link-groups', payload)
    setGroupOpen(false)
    message.success(t('common.saved'))
    load()
  }
  const deleteGroup = async (id: number) => {
    await api.del(`/api/admin/link-groups/${id}`)
    load()
  }
  // Home-page show/hide toggle. Reuses the edit endpoint with the group's current fields so it
  // reads as an inline binary toggle (auto-saves + toasts, per the console save conventions).
  const toggleGroupVisible = async (g: LinkGroup, visible: boolean) => {
    await api.put(`/api/admin/link-groups/${g.id}`, { name: g.name, mode: g.mode, showLabel: g.showLabel, icon: g.icon || '', visible })
    message.success(visible ? t('links.shown') : t('links.hidden'))
    load()
  }

  const openAdd = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ kind: 'url', newTab: true })
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
    })
    setOpen(true)
  }
  const submit = async () => {
    const v = await form.validateFields()
    // Preserve the visibility toggle across an edit (the modal doesn't expose it).
    const visible = editing ? editing.visible !== false : true
    const payload =
      v.kind === 'shortcut'
        ? { label: v.label, url: shortcutUrl(v.shortcut, v.shortcutTarget), icon: v.icon, newTab: false, visible }
        : { label: v.label, url: v.url, icon: v.icon, newTab: v.newTab, visible }
    if (editing) await api.put(`/api/admin/links/${editing.id}`, payload)
    else await api.post('/api/admin/links', payload)
    setOpen(false)
    message.success(t('common.saved'))
    load()
  }
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
  // Home-page show/hide toggle (see toggleGroupVisible) — reuses the edit endpoint with the entry's
  // current fields so a flip is a self-contained auto-save.
  const toggleLinkVisible = async (l: LinkItem, visible: boolean) => {
    await api.put(`/api/admin/links/${l.id}`, { label: l.label, url: l.url, icon: l.icon, newTab: l.newTab !== false, visible })
    message.success(visible ? t('links.shown') : t('links.hidden'))
    load()
  }

  // A link's URL rendered as a link or a shortcut tag.
  const urlDisplay = (u: string) => {
    const res = shortcutOfUrl(u)
    if (!res)
      return (
        <a href={u} target="_blank" rel="noreferrer">
          {u}
        </a>
      )
    const suffix = res.param ? ` · ${targetOptionsFor(res.shortcut.key).find((o) => o.value === res.param)?.label ?? '#' + res.param}` : ''
    return (
      <Tag color="blue">
        {t('links.shortcutTag', { name: t(res.shortcut.labelKey) })}
        {suffix}
      </Tag>
    )
  }

  const groupHeader = (g: LinkGroup) => {
    const GroupIcon = g.icon ? linkIconComponent(g.icon) : FolderOutlined
    const modeLabel = modeOptions.find((m) => m.value === g.mode)?.label ?? g.mode
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          flexWrap: 'wrap',
          padding: '8px 10px',
          borderRadius: 8,
          background: token.colorFillSecondary,
          border: `1px solid ${token.colorBorderSecondary}`,
          opacity: g.visible === false ? 0.55 : 1, // dim a hidden group so its state reads at a glance
        }}
      >
        <DragHandle />
        <GroupIcon style={{ color: token.colorTextTertiary }} />
        <Typography.Text strong={!!g.name} type={g.name ? undefined : 'secondary'}>
          {g.name || t('links.unnamedGroup')}
        </Typography.Text>
        <Tag>{modeLabel}</Tag>
        {g.showLabel && (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t('links.showName')}
          </Typography.Text>
        )}
        <div style={{ flex: 1 }} />
        <Tooltip title={t('links.showOnHome')}>
          <Switch size="small" checked={g.visible !== false} onChange={(v) => toggleGroupVisible(g, v)} />
        </Tooltip>
        <Button size="small" icon={<EditOutlined />} onClick={() => openGroupEdit(g)} />
        <Popconfirm title={t('links.deleteGroupConfirm')} onConfirm={() => deleteGroup(g.id)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      </div>
    )
  }

  const linkRow = (l: LinkItem) => {
    const Icon = linkIconComponent(l.icon)
    const grouped = !!(l.groupId && l.groupId > 0)
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 10,
          padding: '6px 10px',
          paddingInlineStart: grouped ? 30 : 10, // indent grouped links to show nesting
          borderRadius: 8,
          border: `1px solid ${token.colorBorderSecondary}`,
          background: token.colorBgContainer,
          opacity: l.visible === false ? 0.55 : 1, // dim a hidden entry so its state reads at a glance
        }}
      >
        <DragHandle />
        <Icon />
        <Typography.Text style={{ minWidth: 120, flexShrink: 0 }} ellipsis>
          {l.label}
        </Typography.Text>
        <span style={{ flex: 1, minWidth: 0, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{urlDisplay(l.url)}</span>
        {l.newTab === false ? null : (
          <Typography.Text type="secondary" style={{ fontSize: 12, flexShrink: 0 }}>
            {t('links.newTab')}
          </Typography.Text>
        )}
        <Tooltip title={t('links.showOnHome')}>
          <Switch size="small" checked={l.visible !== false} onChange={(v) => toggleLinkVisible(l, v)} />
        </Tooltip>
        <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(l)} />
        <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(l.id)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      </div>
    )
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
        <Typography.Text type="secondary">{t('links.hint')}</Typography.Text>
        <Space>
          <Button icon={<FolderAddOutlined />} onClick={openGroupAdd}>
            {t('links.addGroup')}
          </Button>
          <Button type="primary" icon={<PlusOutlined />} onClick={openAdd}>
            {t('common.add')}
          </Button>
        </Space>
      </Space>

      {!loading && rows.length === 0 ? (
        <Empty description={t('links.empty')} />
      ) : (
        <DndContext sensors={sensors} collisionDetection={closestCenter} modifiers={[restrictToVerticalAxis, restrictToParentElement]} onDragEnd={onDragEnd}>
          <SortableContext items={ids} strategy={verticalListSortingStrategy}>
            <div style={{ display: 'flex', flexDirection: 'column', gap: 6 }}>
              {rows.map((row) => (
                <SortableItem key={row.key} id={row.key}>
                  {row.type === 'group' ? groupHeader(row.group) : linkRow(row.link)}
                </SortableItem>
              ))}
            </div>
          </SortableContext>
        </DndContext>
      )}

      <Modal
        open={groupOpen}
        title={groupEditing ? t('links.editGroup') : t('links.addGroup')}
        onOk={submitGroup}
        onCancel={() => setGroupOpen(false)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={groupForm} layout="vertical">
          <Form.Item name="name" label={t('links.groupName')}>
            <Input placeholder={t('links.groupNamePlaceholder')} />
          </Form.Item>
          <Form.Item name="mode" label={t('links.groupMode')} initialValue="expand">
            <Select<LinkGroupMode> options={modeOptions} />
          </Form.Item>
          <Form.Item name="icon" label={t('links.icon')}>
            <Select allowClear showSearch placeholder={t('links.iconPlaceholder')} options={iconSelectOptions} optionFilterProp="value" />
          </Form.Item>
          <Form.Item name="showLabel" valuePropName="checked" initialValue>
            <Checkbox>{t('links.showName')}</Checkbox>
          </Form.Item>
        </Form>
      </Modal>

      <Modal open={open} title={editing ? t('common.edit') : t('common.add')} onOk={submit} onCancel={() => setOpen(false)} okText={t('common.save')} cancelText={t('common.cancel')} destroyOnClose>
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
                    <Select placeholder={t('links.shortcutPlaceholder')} options={APP_SHORTCUTS.map((s) => ({ value: s.key, label: t(s.labelKey) }))} />
                  </Form.Item>
                  {sc?.hasTarget && (
                    <Form.Item name="shortcutTarget" label={t('links.shortcutTarget')}>
                      <Select allowClear showSearch optionFilterProp="label" placeholder={t('links.shortcutTargetPlaceholder')} options={targetOptionsFor(key)} />
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
        </Form>
      </Modal>
    </Space>
  )
}
