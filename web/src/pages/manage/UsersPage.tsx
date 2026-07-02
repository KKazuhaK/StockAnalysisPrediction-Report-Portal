import { useEffect, useState } from 'react'
import { App, Button, Form, Input, Modal, Popconfirm, Select, Space, Table, Tag } from 'antd'
import { DeleteOutlined, KeyOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { Role, UserRow, UsersResp } from '../../api/types'

export default function UsersPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<UsersResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [addOpen, setAddOpen] = useState(false)
  const [pwUser, setPwUser] = useState<string | null>(null)
  const [addForm] = Form.useForm()
  const [pwForm] = Form.useForm()

  const load = () =>
    api
      .get<UsersResp>('/api/admin/users')
      .then(setData)
      .finally(() => setLoading(false))

  useEffect(() => {
    load()
  }, [])

  const roles: Role[] = data?.roles || []
  const roleOptions = roles.map((r) => ({ value: r.code, label: r.name }))

  const setRole = async (name: string, role: string) => {
    await api.put(`/api/admin/users/${encodeURIComponent(name)}`, { role })
    message.success(t('common.saved'))
    load()
  }

  const addUser = async () => {
    const v = await addForm.validateFields()
    await api.post('/api/admin/users', v)
    setAddOpen(false)
    addForm.resetFields()
    message.success(t('common.done'))
    load()
  }

  const resetPw = async () => {
    const v = await pwForm.validateFields()
    await api.put(`/api/admin/users/${encodeURIComponent(pwUser!)}`, {
      role: data!.users.find((u) => u.username === pwUser)!.role,
      password: v.password,
    })
    setPwUser(null)
    pwForm.resetFields()
    message.success(t('common.saved'))
  }

  const remove = async (name: string) => {
    await api.del(`/api/admin/users/${encodeURIComponent(name)}`)
    load()
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <div style={{ textAlign: 'right' }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setAddOpen(true)}>
          {t('common.add')}
        </Button>
      </div>

      <Table<UserRow>
        rowKey="username"
        loading={loading}
        dataSource={data?.users}
        pagination={false}
        columns={[
          {
            title: t('users.username'),
            dataIndex: 'username',
            render: (u: string) => (
              <Space>
                {u}
                {u === data?.me && <Tag color="green">{t('users.me')}</Tag>}
              </Space>
            ),
          },
          {
            title: t('users.role'),
            dataIndex: 'role',
            width: 200,
            render: (role: string, u) => (
              <Select
                size="small"
                value={role}
                options={roleOptions}
                style={{ width: 160 }}
                onChange={(v) => setRole(u.username, v)}
              />
            ),
          },
          {
            title: '',
            width: 120,
            align: 'right',
            render: (_, u) => (
              <Space>
                <Button size="small" icon={<KeyOutlined />} onClick={() => setPwUser(u.username)} />
                <Popconfirm
                  title={t('common.deleteConfirm')}
                  onConfirm={() => remove(u.username)}
                  disabled={u.username === data?.me}
                >
                  <Button size="small" danger icon={<DeleteOutlined />} disabled={u.username === data?.me} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />

      <Modal
        open={addOpen}
        title={t('common.add')}
        onOk={addUser}
        onCancel={() => setAddOpen(false)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={addForm} layout="vertical" initialValues={{ role: 'user' }}>
          <Form.Item name="username" label={t('users.username')} rules={[{ required: true }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="password" label={t('users.password')} rules={[{ required: true }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="role" label={t('users.role')}>
            <Select options={roleOptions} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={!!pwUser}
        title={`${t('users.newPassword')} — ${pwUser ?? ''}`}
        onOk={resetPw}
        onCancel={() => setPwUser(null)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={pwForm} layout="vertical">
          <Form.Item name="password" label={t('users.newPassword')} rules={[{ required: true }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
