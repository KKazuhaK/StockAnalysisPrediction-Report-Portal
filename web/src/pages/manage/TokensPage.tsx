import { useEffect, useState } from 'react'
import { App, Button, DatePicker, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { TokenRow } from '../../api/types'

const SCOPE_COLORS: Record<string, string> = { all: 'gold', ingest: 'blue', query: 'green' }

// Bearer tokens for the machine API (/api/v1 and the legacy ingest/query routes).
// Scopes: all / ingest / query. Tokens are shown masked and copyable.
export default function TokensPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [tokens, setTokens] = useState<TokenRow[]>([])
  const [loading, setLoading] = useState(true)
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState<number[]>([])
  const [form] = Form.useForm()

  const load = () =>
    api
      .get<{ tokens: TokenRow[] }>('/api/admin/tokens')
      .then((r) => setTokens(r.tokens || []))
      .finally(() => setLoading(false))

  useEffect(() => {
    load()
  }, [])

  const openAdd = () => {
    form.resetFields()
    form.setFieldsValue({ scope: 'all' })
    setOpen(true)
  }

  const create = async () => {
    const v = await form.validateFields()
    await api.post('/api/admin/tokens', {
      name: v.name || '',
      scope: v.scope || 'all',
      expires: v.expires ? v.expires.format('YYYY-MM-DD') : '',
    })
    setOpen(false)
    message.success(t('common.done'))
    load()
  }

  const remove = async (id: number) => {
    await api.del(`/api/admin/tokens/${id}`)
    load()
  }

  const removeSelected = async () => {
    await Promise.all(selected.map((id) => api.del(`/api/admin/tokens/${id}`)))
    setSelected([])
    message.success(t('common.done'))
    load()
  }

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

      <Table<TokenRow>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={tokens}
        pagination={false}
        scroll={{ x: 'max-content' }}
        rowSelection={{ selectedRowKeys: selected, onChange: (keys) => setSelected(keys as number[]) }}
        columns={[
          { title: t('settings.tokenName'), dataIndex: 'name', render: (n: string) => n || '—' },
          {
            title: t('settings.tokenValue'),
            dataIndex: 'token',
            render: (tok: string) => (
              <Typography.Text copyable={{ text: tok }} style={{ fontFamily: 'monospace' }}>
                {tok.slice(0, 8)}…{tok.slice(-4)}
              </Typography.Text>
            ),
          },
          {
            title: t('settings.tokenScope'),
            dataIndex: 'scope',
            render: (s: string) => <Tag color={SCOPE_COLORS[s] || 'default'}>{t(`scope.${s}`, s)}</Tag>,
          },
          { title: t('settings.tokenExpires'), dataIndex: 'expires', render: (e: string) => e || '∞' },
          { title: t('settings.tokenLastUsed'), dataIndex: 'lastUsed', render: (e: string) => e || '—' },
          {
            title: '',
            width: 60,
            align: 'right',
            render: (_, r) => (
              <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(r.id)}>
                <Button size="small" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            ),
          },
        ]}
      />

      <Modal
        open={open}
        title={t('settings.tokens')}
        onOk={create}
        onCancel={() => setOpen(false)}
        okText={t('common.add')}
        cancelText={t('common.cancel')}
        destroyOnHidden
      >
        <Form form={form} layout="vertical" initialValues={{ scope: 'all' }}>
          <Form.Item name="name" label={t('settings.tokenName')}>
            <Input placeholder={t('settings.tokenName')} />
          </Form.Item>
          <Form.Item name="scope" label={t('settings.tokenScope')}>
            <Select
              options={[
                { value: 'all', label: t('scope.all') },
                { value: 'ingest', label: t('scope.ingest') },
                { value: 'query', label: t('scope.query') },
              ]}
            />
          </Form.Item>
          <Form.Item name="expires" label={t('settings.tokenExpires')}>
            <DatePicker style={{ width: '100%' }} />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
