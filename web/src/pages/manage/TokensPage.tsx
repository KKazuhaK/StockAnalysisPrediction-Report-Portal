import { useEffect, useState } from 'react'
import { App, Button, DatePicker, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd'
import { CopyOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { TokenRow } from '../../api/types'

const SCOPE_COLORS: Record<string, string> = { all: 'gold', ingest: 'blue', query: 'green' }

// A token is a fixed-width secret: proportional type makes it unreadable and unverifiable.
const MONO = 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace'

// Bearer tokens for the machine API (/api/v1 and the legacy ingest/query routes).
// Scopes: all / ingest / query. A secret is shown once on creation; the table keeps only its prefix.
export default function TokensPage() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
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
    const result = await api.post<{ token: string }>('/api/admin/tokens', {
      name: v.name || '',
      scope: v.scope || 'all',
      expires: v.expires ? v.expires.format('YYYY-MM-DD') : '',
    })
    setOpen(false)
    // The secret is shown exactly once, so this dialog is the only chance to get it out intact.
    // It gets a read-only field rather than running text: a field never breaks the token across
    // lines, selects itself on focus, and scrolls instead of wrapping when the modal is narrow.
    modal.success({
      title: t('settings.tokenCreated'),
      // Sized so all 48 monospace chars clear the copy button and the confirm icon's indent (~40px
      // of slack measured against SF Mono, the widest of the stack). Should a font ever overrun it
      // anyway, the field scrolls and Copy still yields the whole secret — it degrades, not breaks.
      width: 640,
      content: (
        <Space direction="vertical" size={12} style={{ width: '100%', marginTop: 8 }}>
          <Space.Compact style={{ width: '100%' }}>
            <Input readOnly value={result.token} onFocus={(e) => e.currentTarget.select()} style={{ fontFamily: MONO }} />
            <Button icon={<CopyOutlined />} onClick={() => copyToken(result.token)}>
              {t('common.copy')}
            </Button>
          </Space.Compact>
          <Typography.Text type="warning" style={{ fontSize: 12 }}>
            {t('settings.tokenCreatedHint')}
          </Typography.Text>
        </Space>
      ),
    })
    load()
  }

  // navigator.clipboard needs a secure context; where it is unavailable the field above is still
  // focus-to-select, so the token stays recoverable by hand rather than silently lost.
  const copyToken = async (token: string) => {
    try {
      await navigator.clipboard.writeText(token)
      message.success(t('common.done'))
    } catch {
      message.warning(t('settings.tokenCreatedHint'))
    }
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
            dataIndex: 'prefix',
            render: (prefix: string) => (
              <Typography.Text type="secondary" style={{ fontFamily: MONO }}>
                {prefix}…
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
