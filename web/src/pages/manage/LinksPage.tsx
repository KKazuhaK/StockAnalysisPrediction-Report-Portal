import { useEffect, useState } from 'react'
import { App, Button, Form, Input, Modal, Popconfirm, Space, Table, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { LinkItem } from '../../api/types'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'

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
    setOpen(true)
  }
  const openEdit = (l: LinkItem) => {
    setEditing(l)
    form.setFieldsValue({ label: l.label, url: l.url })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    if (editing) await api.put(`/api/admin/links/${editing.id}`, v)
    else await api.post('/api/admin/links', v)
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
            { title: t('links.label'), dataIndex: 'label' },
            {
              title: t('links.url'),
              dataIndex: 'url',
              render: (u: string) => (
                <a href={u} target="_blank" rel="noreferrer">
                  {u}
                </a>
              ),
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
          <Form.Item name="url" label={t('links.url')} rules={[{ required: true }]}>
            <Input placeholder="https://…" />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
