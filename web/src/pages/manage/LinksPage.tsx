import { useEffect, useState } from 'react'
import { App, Button, Checkbox, Form, Input, Modal, Popconfirm, Radio, Select, Space, Table, Tag, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { LinkItem } from '../../api/types'
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
  const [links, setLinks] = useState<LinkItem[]>([])
  const [loading, setLoading] = useState(true)
  const [editing, setEditing] = useState<LinkItem | null>(null)
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm()

  const load = () =>
    api
      .get<{ links: LinkItem[] }>('/api/admin/links')
      .then((r) => setLinks(r.links || []))
      .finally(() => setLoading(false))

  useEffect(() => {
    load()
  }, [])

  const openAdd = () => {
    setEditing(null)
    form.resetFields()
    form.setFieldsValue({ kind: 'url', newTab: true }) // default: an external link, new tab
    setOpen(true)
  }
  const openEdit = (l: LinkItem) => {
    setEditing(l)
    const sc = shortcutOfUrl(l.url)
    form.setFieldsValue({
      label: l.label,
      icon: l.icon,
      kind: sc ? 'shortcut' : 'url',
      shortcut: sc?.key,
      url: sc ? '' : l.url,
      newTab: l.newTab !== false,
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    // A shortcut is stored as url = "rp:<key>"; a plain link keeps its URL + new-tab flag.
    const payload =
      v.kind === 'shortcut'
        ? { label: v.label, url: shortcutUrl(v.shortcut), icon: v.icon, newTab: false }
        : { label: v.label, url: v.url, icon: v.icon, newTab: v.newTab }
    if (editing) await api.put(`/api/admin/links/${editing.id}`, payload)
    else await api.post('/api/admin/links', payload)
    setOpen(false)
    message.success(t('common.saved'))
    load()
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
                const sc = shortcutOfUrl(u)
                return sc ? (
                  <Tag color="blue">{t('links.shortcutTag', { name: t(sc.labelKey) })}</Tag>
                ) : (
                  <a href={u} target="_blank" rel="noreferrer">
                    {u}
                  </a>
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
        <Form form={form} layout="vertical">
          <Form.Item name="label" label={t('links.label')} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="kind" label={t('links.type')} initialValue="url">
            <Radio.Group optionType="button" buttonStyle="solid">
              <Radio.Button value="url">{t('links.typeUrl')}</Radio.Button>
              <Radio.Button value="shortcut">{t('links.typeShortcut')}</Radio.Button>
            </Radio.Group>
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(a, b) => a.kind !== b.kind}>
            {({ getFieldValue }) =>
              getFieldValue('kind') === 'shortcut' ? (
                <Form.Item name="shortcut" label={t('links.shortcut')} rules={[{ required: true }]}>
                  <Select
                    placeholder={t('links.shortcutPlaceholder')}
                    options={APP_SHORTCUTS.map((s) => ({ value: s.key, label: t(s.labelKey) }))}
                  />
                </Form.Item>
              ) : (
                <Form.Item name="url" label={t('links.url')} rules={[{ required: true }]}>
                  <Input placeholder="https://…" />
                </Form.Item>
              )
            }
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
