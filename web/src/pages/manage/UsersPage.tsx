import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Avatar,
  Button,
  Card,
  Dropdown,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  PlusOutlined,
  SearchOutlined,
  ThunderboltOutlined,
  UsergroupAddOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { priorityNum } from '../../lib/batchUi'
import type { Role, UserGroupRow, UserRow, UsersResp } from '../../api/types'

// A deterministic avatar colour from a name, so each user reads distinctly.
const ROLE_COLOR: Record<string, string> = { admin: 'gold', operator: 'blue', user: 'default' }
const AVATAR_COLORS = ['#1677ff', '#52c41a', '#faad14', '#eb2f96', '#722ed1', '#13c2c2', '#fa541c']
function avatarColor(s: string) {
  let h = 0
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0
  return AVATAR_COLORS[h % AVATAR_COLORS.length]
}
function initials(s: string) {
  const t = s.trim()
  if (!t) return '?'
  // First glyph works for CJK; for latin words take up to two initials.
  const parts = t.split(/\s+/)
  if (parts.length > 1) return (parts[0][0] + parts[1][0]).toUpperCase()
  return t.slice(0, /[一-龥]/.test(t) ? 1 : 2).toUpperCase()
}

export default function UsersPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<UsersResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [search, setSearch] = useState('')
  const [roleFilter, setRoleFilter] = useState<string>()
  const [groupFilter, setGroupFilter] = useState<number>()
  const [selected, setSelected] = useState<string[]>([])
  const [editUser, setEditUser] = useState<UserRow | 'new' | null>(null)
  const [pwUser, setPwUser] = useState<string | null>(null)
  const [form] = Form.useForm()
  const [pwForm] = Form.useForm()

  const load = () =>
    api
      .get<UsersResp>('/api/admin/users')
      .then((d) => {
        setData(d)
        setSelected((sel) => sel.filter((u) => d.users.some((x) => x.username === u)))
      })
      .finally(() => setLoading(false))
  useEffect(() => {
    load()
  }, [])

  const roles: Role[] = data?.roles || []
  const groups: UserGroupRow[] = data?.groups || []
  const roleName = (code: string) => roles.find((r) => r.code === code)?.name || code
  const groupById = useMemo(() => new Map(groups.map((g) => [g.id, g])), [groups])
  const adminCount = useMemo(() => (data?.users || []).filter((u) => u.role === 'admin').length, [data])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (data?.users || []).filter((u) => {
      if (roleFilter && u.role !== roleFilter) return false
      if (groupFilter && !u.groups.includes(groupFilter)) return false
      if (q && ![u.username, u.display_name, u.email].some((v) => (v || '').toLowerCase().includes(q))) return false
      return true
    })
  }, [data, search, roleFilter, groupFilter])

  const patch = async (name: string, body: Record<string, unknown>) => {
    await api.put(`/api/admin/users/${encodeURIComponent(name)}`, body)
    load()
  }
  const bulk = async (action: string, extra: Record<string, unknown> = {}) => {
    const r = await api.post<{ n: number }>('/api/admin/users/bulk', { action, usernames: selected, ...extra })
    message.success(t('users.bulkDone', { n: r.n }))
    setSelected([])
    load()
  }

  const openEdit = (u: UserRow | 'new') => {
    setEditUser(u)
    if (u === 'new') form.setFieldsValue({ username: '', display_name: '', email: '', role: 'user', groups: [], password: '' })
    else form.setFieldsValue({ username: u.username, display_name: u.display_name, email: u.email, role: u.role, groups: u.groups, password: '' })
  }
  const saveEdit = async () => {
    const v = await form.validateFields()
    try {
      if (editUser === 'new') {
        await api.post('/api/admin/users', {
          username: v.username,
          password: v.password,
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          groups: v.groups || [],
        })
      } else {
        await patch((editUser as UserRow).username, {
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          groups: v.groups || [],
          ...(v.password ? { password: v.password } : {}),
        })
      }
      setEditUser(null)
      message.success(t('common.saved'))
      load()
    } catch (e) {
      message.error((e as Error).message)
    }
  }
  const resetPw = async () => {
    const v = await pwForm.validateFields()
    await patch(pwUser!, { password: v.password })
    setPwUser(null)
    pwForm.resetFields()
    message.success(t('common.saved'))
  }

  const cols: ColumnsType<UserRow> = [
    {
      title: t('users.user'),
      dataIndex: 'username',
      render: (_, u) => (
        <Space>
          <Avatar style={{ backgroundColor: avatarColor(u.username), flexShrink: 0 }}>{initials(u.display_name || u.username)}</Avatar>
          <div style={{ lineHeight: 1.3 }}>
            <Space size={6}>
              <Typography.Text strong>{u.display_name || u.username}</Typography.Text>
              {u.username === data?.me && <Tag color="green">{t('users.me')}</Tag>}
            </Space>
            <div>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                @{u.username}
                {u.email ? ` · ${u.email}` : ''}
              </Typography.Text>
            </div>
          </div>
        </Space>
      ),
    },
    {
      title: t('users.role'),
      dataIndex: 'role',
      width: 110,
      render: (role: string) => <Tag color={ROLE_COLOR[role] ?? 'default'}>{roleName(role)}</Tag>,
    },
    {
      title: t('users.groups'),
      dataIndex: 'groups',
      render: (gids: number[]) =>
        gids.length ? (
          <Space size={[4, 4]} wrap>
            {gids.map((id) => (
              <Tag key={id} color="blue">
                {groupById.get(id)?.name || id}
              </Tag>
            ))}
          </Space>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        ),
    },
    {
      title: t('users.status'),
      dataIndex: 'active',
      width: 76,
      render: (active: boolean, u) => {
        const isLastAdmin = u.role === 'admin' && adminCount <= 1
        return (
          <Tooltip title={active ? t('users.active') : t('users.disabled')}>
            <Switch
              size="small"
              checked={active}
              disabled={u.username === data?.me || isLastAdmin}
              onChange={(checked) => patch(u.username, { active: checked })}
            />
          </Tooltip>
        )
      },
    },
    {
      title: t('users.lastLogin'),
      dataIndex: 'last_login',
      width: 160,
      render: (v: string) =>
        v ? <Typography.Text style={{ fontSize: 12 }}>{v}</Typography.Text> : <Typography.Text type="secondary">{t('users.never')}</Typography.Text>,
    },
    {
      title: '',
      width: 120,
      align: 'right',
      render: (_, u) => (
        <Space>
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(u)} />
          <Button size="small" icon={<KeyOutlined />} onClick={() => setPwUser(u.username)} />
          <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(u.username)} disabled={u.username === data?.me}>
            <Button size="small" danger icon={<DeleteOutlined />} disabled={u.username === data?.me} />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const remove = async (name: string) => {
    await api.del(`/api/admin/users/${encodeURIComponent(name)}`)
    load()
  }

  const accounts = (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Space wrap style={{ width: '100%', justifyContent: 'space-between' }}>
        <Space wrap>
          <Input
            allowClear
            prefix={<SearchOutlined />}
            placeholder={t('users.search')}
            style={{ width: 240 }}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
          <Select
            allowClear
            placeholder={t('users.role')}
            style={{ width: 130 }}
            value={roleFilter}
            onChange={setRoleFilter}
            options={roles.map((r) => ({ value: r.code, label: r.name }))}
          />
          <Select
            allowClear
            placeholder={t('users.groups')}
            style={{ width: 150 }}
            value={groupFilter}
            onChange={setGroupFilter}
            options={groups.map((g) => ({ value: g.id, label: g.name }))}
          />
        </Space>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => openEdit('new')}>
          {t('users.add')}
        </Button>
      </Space>

      {selected.length > 0 && (
        <Card size="small" style={{ background: 'var(--ant-color-fill-quaternary)' }}>
          <Space wrap>
            <Typography.Text strong>{t('users.selectedN', { n: selected.length })}</Typography.Text>
            <Button size="small" onClick={() => bulk('enable')}>
              {t('users.active')}
            </Button>
            <Button size="small" onClick={() => bulk('disable')}>
              {t('users.disabled')}
            </Button>
            <Dropdown
              menu={{ items: roles.map((r) => ({ key: r.code, label: r.name, onClick: () => bulk('set_role', { role: r.code }) })) }}
            >
              <Button size="small">{t('users.bulkSetRole')}</Button>
            </Dropdown>
            <Dropdown
              menu={{ items: groups.map((g) => ({ key: g.id, label: g.name, onClick: () => bulk('add_group', { group_id: g.id }) })) }}
              disabled={groups.length === 0}
            >
              <Button size="small">{t('users.bulkAddGroup')}</Button>
            </Dropdown>
            <Popconfirm title={t('users.bulkDeleteConfirm', { n: selected.length })} onConfirm={() => bulk('delete')}>
              <Button size="small" danger>
                {t('common.delete')}
              </Button>
            </Popconfirm>
          </Space>
        </Card>
      )}

      <Table<UserRow>
        rowKey="username"
        loading={loading}
        dataSource={filtered}
        columns={cols}
        pagination={false}
        rowSelection={{ selectedRowKeys: selected, onChange: (keys) => setSelected(keys as string[]) }}
      />

      {/* add / edit user */}
      <Modal
        open={editUser != null}
        title={editUser === 'new' ? t('users.add') : t('users.edit')}
        onOk={saveEdit}
        onCancel={() => setEditUser(null)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="username" label={t('users.username')} rules={[{ required: true }]}>
            <Input autoComplete="off" disabled={editUser !== 'new'} />
          </Form.Item>
          <Form.Item name="display_name" label={t('users.displayName')}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="email" label={t('users.email')} rules={[{ type: 'email', message: t('users.emailInvalid') }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="password" label={editUser === 'new' ? t('users.password') : t('users.newPassword')} rules={editUser === 'new' ? [{ required: true }] : []}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item name="role" label={t('users.role')}>
            <Select options={roles.map((r) => ({ value: r.code, label: r.name }))} />
          </Form.Item>
          <Form.Item name="groups" label={t('users.groups')}>
            <Select mode="multiple" allowClear placeholder={t('users.groups')} options={groups.map((g) => ({ value: g.id, label: g.name }))} />
          </Form.Item>
        </Form>
      </Modal>

      {/* reset password */}
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

  return (
    <Tabs
      defaultActiveKey="accounts"
      items={[
        { key: 'accounts', label: t('users.tabAccounts'), children: accounts },
        { key: 'groups', label: t('users.tabGroups'), children: <GroupsPanel groups={groups} onChanged={load} /> },
      ]}
    />
  )
}

// Groups list + editor, shown as the "groups" sub-tab of the users page (it used to
// be a Drawer opened from a toolbar button). Weight / priority feed the run queue.
function GroupsPanel({ groups, onChanged }: { groups: UserGroupRow[]; onChanged: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [edit, setEdit] = useState<UserGroupRow | 'new' | null>(null)
  const [form] = Form.useForm()

  const openForm = (g: UserGroupRow | 'new') => {
    setEdit(g)
    if (g === 'new') form.setFieldsValue({ name: '', description: '', weight: 0, priority: undefined })
    else form.setFieldsValue({ name: g.name, description: g.description, weight: g.weight, priority: g.priority ? Number(g.priority) : undefined })
  }
  const save = async () => {
    const v = await form.validateFields()
    // priority is a base number 0..100; the API stores it as a string (ADR 0008).
    const body = { ...v, priority: v.priority == null || v.priority === '' ? '' : String(v.priority) }
    try {
      if (edit === 'new') await api.post('/api/admin/groups', body)
      else await api.put(`/api/admin/groups/${(edit as UserGroupRow).id}`, body)
      setEdit(null)
      onChanged()
      message.success(t('common.saved'))
    } catch (e) {
      message.error((e as Error).message)
    }
  }
  const remove = async (id: number) => {
    await api.del(`/api/admin/groups/${id}`)
    onChanged()
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 720 }}>
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <Button type="primary" icon={<UsergroupAddOutlined />} onClick={() => openForm('new')}>
          {t('users.addGroup')}
        </Button>
      </div>
      {groups.length === 0 ? (
        <Empty description={t('users.noGroups')} />
      ) : (
        <Space direction="vertical" size={8} style={{ width: '100%' }}>
          {groups.map((g) => (
            <Card key={g.id} size="small">
              <Space style={{ width: '100%', justifyContent: 'space-between' }} align="start">
                <div>
                  <Space size={6} wrap>
                    <Typography.Text strong>{g.name}</Typography.Text>
                    <Tag>{t('users.groupMembers', { n: g.members })}</Tag>
                    {g.weight > 0 && (
                      <Tag color="gold" icon={<ThunderboltOutlined />}>
                        {t('users.weightN', { n: g.weight })}
                      </Tag>
                    )}
                    {g.priority && <Tag color="blue">{t('users.priorityTag', { n: priorityNum(g.priority) })}</Tag>}
                  </Space>
                  {g.description && (
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {g.description}
                      </Typography.Text>
                    </div>
                  )}
                </div>
                <Space>
                  <Button size="small" icon={<EditOutlined />} onClick={() => openForm(g)} />
                  <Popconfirm title={t('users.deleteGroupConfirm')} onConfirm={() => remove(g.id)}>
                    <Button size="small" danger icon={<DeleteOutlined />} />
                  </Popconfirm>
                </Space>
              </Space>
            </Card>
          ))}
        </Space>
      )}

      <Modal
        open={edit != null}
        title={edit === 'new' ? t('users.addGroup') : t('users.editGroup')}
        onOk={save}
        onCancel={() => setEdit(null)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label={t('users.groupName')} rules={[{ required: true }]}>
            <Input autoComplete="off" />
          </Form.Item>
          <Form.Item name="description" label={t('users.groupDesc')}>
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="weight" label={t('users.weight')} extra={t('users.weightHint')}>
            <InputNumber min={0} max={999} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="priority" label={t('users.priority')} extra={t('users.priorityHint')}>
            <InputNumber min={0} max={100} style={{ width: '100%' }} placeholder={t('users.prioritySystemDefault')} />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )
}
