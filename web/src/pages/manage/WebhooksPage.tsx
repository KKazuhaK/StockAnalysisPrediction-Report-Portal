import { useEffect, useState } from 'react'
import { App, Button, Card, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { ApiOutlined, DeleteOutlined, PlusOutlined, SendOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { Webhook, WebhooksResp } from '../../api/types'

export default function WebhooksPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [hooks, setHooks] = useState<Webhook[]>([])
  const [events, setEvents] = useState<string[]>([])
  const [open, setOpen] = useState(false)
  const [form] = Form.useForm()

  const load = () =>
    api.get<WebhooksResp>('/api/admin/webhooks').then((r) => {
      setHooks(r.webhooks || [])
      setEvents(r.events || [])
    })
  useEffect(() => {
    load()
  }, [])

  const eventLabel = (code: string) => t(`webhook.event.${code}`, code)

  const submit = async () => {
    const v = await form.validateFields()
    await api.post('/api/admin/webhooks', { url: v.url, events: v.events, secret: v.secret || '' })
    setOpen(false)
    message.success(t('webhook.msgAdded'))
    load()
  }

  const test = async (id: number) => {
    const r = await api.post<{ ok: boolean; status: number; error: string }>(`/api/admin/webhooks/${id}/test`)
    if (r.ok) message.success(t('webhook.msgTested', { status: r.status }))
    else message.error(t('webhook.msgTestFailed', { error: r.error || r.status }))
    load()
  }

  const remove = async (id: number) => {
    await api.del(`/api/admin/webhooks/${id}`)
    load()
  }

  const statusTag = (h: Webhook) => {
    if (!h.last_delivered_at) return <Typography.Text type="secondary">{t('webhook.never')}</Typography.Text>
    const ok = h.last_status >= 200 && h.last_status < 400
    const label = h.last_status ? String(h.last_status) : h.last_error || '—'
    return (
      <Tag color={ok ? 'success' : 'error'} title={h.last_error}>
        {label}
      </Tag>
    )
  }

  const cols: ColumnsType<Webhook> = [
    { title: t('webhook.url'), dataIndex: 'url', ellipsis: true },
    {
      title: t('webhook.events'),
      dataIndex: 'events',
      width: 220,
      render: (evs: string[]) => (evs || []).map((e) => <Tag key={e}>{eventLabel(e)}</Tag>),
    },
    { title: t('webhook.lastStatus'), width: 200, render: (_: unknown, h: Webhook) => statusTag(h) },
    {
      title: t('batch.col.actions'),
      width: 150,
      render: (_: unknown, h: Webhook) => (
        <Space>
          <Button size="small" icon={<SendOutlined />} onClick={() => test(h.id)}>
            {t('webhook.test')}
          </Button>
          <Popconfirm title={t('webhook.deleteConfirm')} onConfirm={() => remove(h.id)}>
            <Button size="small" danger icon={<DeleteOutlined />} />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  return (
    <Card
      title={
        <Space>
          <ApiOutlined />
          {t('webhook.title')}
        </Space>
      }
      extra={
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            form.resetFields()
            setOpen(true)
          }}
        >
          {t('webhook.add')}
        </Button>
      }
    >
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text type="secondary">{t('webhook.hint')}</Typography.Text>
        <Table rowKey="id" size="small" dataSource={hooks} columns={cols} pagination={false} scroll={{ x: 'max-content' }} />
      </Space>

      <Modal title={t('webhook.add')} open={open} onOk={submit} onCancel={() => setOpen(false)} destroyOnHidden>
        <Form form={form} layout="vertical">
          <Form.Item name="url" label={t('webhook.url')} rules={[{ required: true, message: t('webhook.required') }]}>
            <Input placeholder="https://open.feishu.cn/open-apis/bot/v2/hook/..." />
          </Form.Item>
          <Form.Item name="events" label={t('webhook.events')} rules={[{ required: true, message: t('webhook.required') }]}>
            <Select
              mode="multiple"
              placeholder={t('webhook.events')}
              options={events.map((e) => ({ value: e, label: eventLabel(e) }))}
            />
          </Form.Item>
          <Form.Item name="secret" label={t('webhook.secret')} extra={t('webhook.secretHint')}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </Card>
  )
}
