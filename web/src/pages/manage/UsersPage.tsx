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
  ClockCircleOutlined,
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
import type { BatchConfig, Role, UserGroupRow, UserRow, UsersResp } from '../../api/types'

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
  const defaultGroup = useMemo(() => groups.find((g) => g.is_default), [groups])
  const adminCount = useMemo(() => (data?.users || []).filter((u) => u.role === 'admin').length, [data])

  // A user's effective group is their primary group, or the Default when unassigned.
  const primaryOf = (u: UserRow) => (u.primary_group ? groupById.get(u.primary_group) : undefined)

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (data?.users || []).filter((u) => {
      if (roleFilter && u.role !== roleFilter) return false
      if (groupFilter) {
        const inherits = !u.primary_group && groupFilter === defaultGroup?.id
        if (u.primary_group !== groupFilter && !inherits) return false
      }
      if (q && ![u.username, u.display_name, u.email].some((v) => (v || '').toLowerCase().includes(q))) return false
      return true
    })
  }, [data, search, roleFilter, groupFilter, defaultGroup])

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
    if (u === 'new') form.setFieldsValue({ username: '', display_name: '', email: '', role: 'user', primary_group: undefined, password: '' })
    else
      form.setFieldsValue({
        username: u.username,
        display_name: u.display_name,
        email: u.email,
        role: u.role,
        primary_group: u.primary_group || undefined,
        password: '',
      })
  }
  const saveEdit = async () => {
    const v = await form.validateFields()
    const primaryGroup = v.primary_group ?? 0
    try {
      if (editUser === 'new') {
        await api.post('/api/admin/users', {
          username: v.username,
          password: v.password,
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          primary_group: primaryGroup,
        })
      } else {
        await patch((editUser as UserRow).username, {
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          primary_group: primaryGroup,
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
      title: t('users.group'),
      dataIndex: 'primary_group',
      render: (_, u) => {
        const g = primaryOf(u)
        if (g) return <Tag color="blue">{g.name}</Tag>
        // No primary group → inherits the Default group.
        return (
          <Tag>
            {defaultGroup?.name || t('users.defaultGroupTag')} <span style={{ opacity: 0.6 }}>· {t('users.inheritedTag')}</span>
          </Tag>
        )
      },
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
            showSearch
            optionFilterProp="label"
            allowClear
            placeholder={t('users.group')}
            style={{ width: 160 }}
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
              menu={{ items: groups.map((g) => ({ key: g.id, label: g.name, onClick: () => bulk('set_group', { group_id: g.id }) })) }}
              disabled={groups.length === 0}
            >
              <Button size="small">{t('users.bulkSetGroup')}</Button>
            </Dropdown>
            <Button size="small" onClick={() => bulk('clear_group')}>
              {t('users.bulkClearGroup')}
            </Button>
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
        scroll={{ x: 'max-content' }}
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
          <Form.Item name="primary_group" label={t('users.group')} extra={t('users.primaryGroupHint')}>
            <Select
              allowClear
              placeholder={t('users.inheritDefault')}
              options={groups.map((g) => ({ value: g.id, label: g.is_default ? `${g.name} · ${t('users.defaultGroupTag')}` : g.name }))}
            />
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

// Groups list + editor, shown as the "groups" sub-tab of the users page. A group's
// weight / urgent / priority drive the run queue for its members (group model B): a
// non-default group either overrides a field or inherits it from the Default group,
// which every unassigned user falls back to.
function GroupsPanel({ groups, onChanged }: { groups: UserGroupRow[]; onChanged: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [edit, setEdit] = useState<UserGroupRow | 'new' | null>(null)
  const [form] = Form.useForm()
  const isDefault = edit !== 'new' && !!edit?.is_default
  const urgentInherit = Form.useWatch('urgent_inherit', form)
  const urgentPolicy = Form.useWatch('urgent_policy', form)
  const maxqInherit = Form.useWatch('maxq_inherit', form)
  const windowInherit = Form.useWatch('window_inherit', form)
  const defaultGroup = useMemo(() => groups.find((g) => g.is_default), [groups])

  // The urgent lane + ticket config is GLOBAL (not per-group), but it belongs with the
  // per-group weights conceptually, so it lives here rather than on the run-queue
  // settings page. Loaded from / saved to the shared batch config.
  const [urgentEnabled, setUrgentEnabled] = useState(false)
  const [reservedSlots, setReservedSlots] = useState(1)
  const [ticketPeriod, setTicketPeriod] = useState(7)
  const [maxJobs, setMaxJobs] = useState(1)
  const [cfgReady, setCfgReady] = useState(false)

  const loadCfg = () =>
    api.get<BatchConfig>('/api/admin/batch/config').then(
      (r) => {
        setUrgentEnabled(!!r.urgent_enabled)
        setReservedSlots(r.reserved_slots)
        setTicketPeriod(r.ticket_period_days)
        setMaxJobs(r.max_jobs)
        setCfgReady(true)
      },
      () => {
        /* keep group editing usable even if the global queue config is temporarily unreachable */
      },
    )
  useEffect(() => {
    loadCfg()
  }, [])

  const saveUrgent = async () => {
    try {
      await api.post('/api/admin/batch/config', {
        urgent_enabled: urgentEnabled,
        reserved_slots: reservedSlots,
        ticket_period_days: ticketPeriod,
      })
      message.success(t('common.saved'))
      loadCfg()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const openForm = (g: UserGroupRow | 'new') => {
    setEdit(g)
    if (g === 'new')
      form.setFieldsValue({
        name: '', description: '', urgent_inherit: true, urgent_policy: 'ticket', weight: 0,
        maxq_inherit: true, max_queued: 0, window_inherit: true, run_window: '', priority: undefined,
      })
    else
      form.setFieldsValue({
        name: g.name,
        description: g.description,
        // A null field means the group inherits the Default group's value. The urgent
        // policy folds allow_urgent + urgent_unlimited into one 3-way choice.
        urgent_inherit: !g.is_default && g.allow_urgent == null,
        urgent_policy: g.allow_urgent === false ? 'off' : g.urgent_unlimited ? 'unlimited' : 'ticket',
        weight: g.weight ?? 0,
        maxq_inherit: !g.is_default && g.max_queued == null,
        max_queued: g.max_queued ?? 0,
        window_inherit: !g.is_default && g.run_window == null,
        run_window: g.run_window ?? '',
        priority: g.priority ? Number(g.priority) : undefined,
      })
  }
  const save = async () => {
    const v = await form.validateFields()
    const target = edit !== 'new' && edit ? edit : null
    const isDef = !!target?.is_default
    // The urgent policy is one control (禁止 / 限量-次票 / 无限) mapped back to the three
    // stored fields, so allow + unlimited can never contradict. null = inherit Default.
    const urgentInh = !isDef && v.urgent_inherit
    const policy = v.urgent_policy as 'off' | 'ticket' | 'unlimited'
    const body = {
      name: v.name,
      description: v.description || '',
      allow_urgent: urgentInh ? null : policy !== 'off',
      urgent_unlimited: urgentInh ? null : policy === 'unlimited',
      weight: urgentInh ? null : policy === 'ticket' ? (v.weight ?? 0) : 0,
      max_queued: !isDef && v.maxq_inherit ? null : (v.max_queued ?? 0),
      run_window: !isDef && v.window_inherit ? null : (v.run_window || ''),
      priority: v.priority == null || v.priority === '' ? '' : String(v.priority),
    }
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
    try {
      await api.del(`/api/admin/groups/${id}`)
      onChanged()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  // Effective weight / urgent for display: a group's own value, or the Default's when inherited.
  const effWeight = (g: UserGroupRow) => (g.weight != null ? g.weight : defaultGroup?.weight ?? 0)
  const effUrgent = (g: UserGroupRow) => (g.urgent_unlimited != null ? g.urgent_unlimited : !!defaultGroup?.urgent_unlimited)
  const effAllow = (g: UserGroupRow) => (g.allow_urgent != null ? g.allow_urgent : defaultGroup?.allow_urgent !== false)
  const effMaxQueued = (g: UserGroupRow) => (g.max_queued != null ? g.max_queued : defaultGroup?.max_queued ?? 0)
  const effWindow = (g: UserGroupRow) => (g.run_window != null ? g.run_window : defaultGroup?.run_window ?? '')

  const cfgRow = (label: string, hint: string, control: React.ReactNode) => (
    <Space wrap align="start">
      <span style={{ display: 'inline-block', minWidth: 128 }}>{label}</span>
      {control}
      <Typography.Text type="secondary" style={{ maxWidth: 360, display: 'inline-block' }}>
        {hint}
      </Typography.Text>
    </Space>
  )

  return (
    <Space direction="vertical" size={16} style={{ width: '100%', maxWidth: 720 }}>
      {/* Global urgent lane + ticket config (moved off the run-queue settings page). */}
      <Card size="small" title={t('users.urgentTitle')}>
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          {cfgRow(
            t('batch.admin.urgentEnabled'),
            t('batch.admin.urgentEnabledHint'),
            <Switch checked={urgentEnabled} onChange={setUrgentEnabled} disabled={!cfgReady} />,
          )}
          {urgentEnabled && (
            <>
              {cfgRow(
                t('batch.admin.reservedSlots'),
                t('batch.admin.reservedSlotsHint'),
                <InputNumber min={0} max={Math.max(0, maxJobs - 1)} value={reservedSlots} onChange={(v) => setReservedSlots(v ?? 0)} disabled={!cfgReady} />,
              )}
              {cfgRow(
                t('batch.admin.ticketPeriod'),
                t('batch.admin.ticketPeriodHint'),
                <InputNumber
                  min={1}
                  max={365}
                  value={ticketPeriod}
                  onChange={(v) => setTicketPeriod(v || 7)}
                  addonAfter={t('batch.admin.days')}
                  disabled={!cfgReady}
                />,
              )}
            </>
          )}
          <Button type="primary" onClick={saveUrgent} disabled={!cfgReady}>
            {t('common.save')}
          </Button>
        </Space>
      </Card>

      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        <Button type="primary" icon={<UsergroupAddOutlined />} onClick={() => openForm('new')}>
          {t('users.addGroup')}
        </Button>
      </div>
      {groups.length === 0 ? (
        <Empty description={t('users.noGroups')} />
      ) : (
        <Space direction="vertical" size={8} style={{ width: '100%' }}>
          {groups.map((g) => {
            const weightInh = !g.is_default && g.weight == null
            const urgentInh = !g.is_default && g.urgent_unlimited == null
            const w = effWeight(g)
            const urg = effUrgent(g)
            return (
              <Card key={g.id} size="small">
                <Space style={{ width: '100%', justifyContent: 'space-between' }} align="start">
                  <div>
                    <Space size={6} wrap>
                      <Typography.Text strong>{g.name}</Typography.Text>
                      {g.is_default && <Tag color="green">{t('users.defaultGroupTag')}</Tag>}
                      <Tag>{t('users.groupMembers', { n: g.members })}</Tag>
                      {w > 0 && (
                        <Tag color="gold" icon={<ThunderboltOutlined />}>
                          {t('users.weightN', { n: w })}
                          {weightInh && <span style={{ opacity: 0.6 }}> · {t('users.inheritedTag')}</span>}
                        </Tag>
                      )}
                      {urg && (
                        <Tag color="red" icon={<ThunderboltOutlined />}>
                          {t('users.urgentUnlimitedTag')}
                          {urgentInh && <span style={{ opacity: 0.6 }}> · {t('users.inheritedTag')}</span>}
                        </Tag>
                      )}
                      {g.priority && <Tag color="blue">{t('users.priorityTag', { n: priorityNum(g.priority) })}</Tag>}
                      {!effAllow(g) && <Tag>{t('users.urgentDisabledTag')}</Tag>}
                      {effMaxQueued(g) > 0 && <Tag color="geekblue">{t('users.maxQueuedTag', { n: effMaxQueued(g) })}</Tag>}
                      {effWindow(g) && (
                        <Tag color="purple">
                          <ClockCircleOutlined /> {effWindow(g)}
                        </Tag>
                      )}
                    </Space>
                    <div>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {g.is_default ? g.description || t('users.defaultGroupHint') : g.description}
                      </Typography.Text>
                    </div>
                  </div>
                  <Space>
                    <Button size="small" icon={<EditOutlined />} onClick={() => openForm(g)} />
                    {!g.is_default && (
                      <Popconfirm title={t('users.deleteGroupConfirm')} onConfirm={() => remove(g.id)}>
                        <Button size="small" danger icon={<DeleteOutlined />} />
                      </Popconfirm>
                    )}
                  </Space>
                </Space>
              </Card>
            )
          })}
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
          <Form.Item name="description" label={t('users.groupDesc')} extra={isDefault ? t('users.defaultGroupHint') : undefined}>
            <Input.TextArea rows={2} />
          </Form.Item>

          {/* Urgent policy: one 3-way control (disabled / ticketed / unlimited) so allow +
              unlimited can't contradict. A non-default group may inherit the Default's. */}
          {!isDefault && (
            <Form.Item name="urgent_inherit" valuePropName="checked" label={t('users.inheritFromDefault')} style={{ marginBottom: 8 }}>
              <Switch size="small" />
            </Form.Item>
          )}
          <Form.Item name="urgent_policy" label={t('users.urgentPolicy')} extra={t('users.urgentPolicyHint')}>
            <Select
              disabled={!isDefault && urgentInherit}
              options={[
                { value: 'off', label: t('users.urgentOff') },
                { value: 'ticket', label: t('users.urgentTicket') },
                { value: 'unlimited', label: t('users.urgentUnlimitedOpt') },
              ]}
            />
          </Form.Item>
          {urgentPolicy === 'ticket' && (
            <Form.Item name="weight" label={t('users.weight')} extra={t('users.weightHint')}>
              <InputNumber min={0} max={999} style={{ width: '100%' }} disabled={!isDefault && urgentInherit} />
            </Form.Item>
          )}

          {/* Max active (queued + running) runs per member; 0 = unlimited. */}
          {!isDefault && (
            <Form.Item name="maxq_inherit" valuePropName="checked" label={t('users.inheritFromDefault')} style={{ marginBottom: 8 }}>
              <Switch size="small" />
            </Form.Item>
          )}
          <Form.Item name="max_queued" label={t('users.maxQueued')} extra={t('users.maxQueuedHint')}>
            <InputNumber min={0} max={999} style={{ width: '100%' }} disabled={!isDefault && maxqInherit} />
          </Form.Item>

          {/* Run window: allowed hours in panel time, e.g. 9-18. Empty = any hour. */}
          {!isDefault && (
            <Form.Item name="window_inherit" valuePropName="checked" label={t('users.inheritFromDefault')} style={{ marginBottom: 8 }}>
              <Switch size="small" />
            </Form.Item>
          )}
          <Form.Item name="run_window" label={t('users.runWindow')} extra={t('users.runWindowHint')}>
            <Input placeholder="9-18" style={{ width: '100%' }} disabled={!isDefault && windowInherit} />
          </Form.Item>

          {/* The Default group has no priority override — its members use the system default. */}
          {!isDefault && (
            <Form.Item name="priority" label={t('users.priority')} extra={t('users.priorityHint')}>
              <InputNumber min={0} max={100} style={{ width: '100%' }} placeholder={t('users.prioritySystemDefault')} />
            </Form.Item>
          )}
        </Form>
      </Modal>
    </Space>
  )
}
