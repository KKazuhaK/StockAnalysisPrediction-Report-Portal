import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Avatar,
  Button,
  Card,
  DatePicker,
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
import dayjs from 'dayjs'
import type { ColumnsType } from 'antd/es/table'
import {
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  PlusOutlined,
  SearchOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import OrgUnitPicker, { subtreeOf } from './OrgUnitPicker'
import OrgUnitDetail from './OrgUnitDetail'
import LoadGate from '../../components/LoadGate'
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
  const [settled, setSettled] = useState(false) // the first /api/admin/users call has come back, one way or another
  const [search, setSearch] = useState('')
  const [roleFilter, setRoleFilter] = useState<string>()
  // OU scoping (the tree picker). `ouScoped` is the all-vs-selected radio; `ouSelected` is what the
  // tree has highlighted. Kept apart so clearing the selection does not silently widen the scope
  // back to every account, and so the radio survives a search that hides the selected node.
  const [ouScoped, setOuScoped] = useState(false)
  const [ouSelected, setOuSelected] = useState<number[]>([])
  const [tab, setTab] = useState('accounts')
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
      // A failed load leaves `data` null for good, and the OU tree beside the table reads that as
      // "still loading". Settling it here means the tree falls back to its empty text next to a
      // table that is also visibly empty — one page in one state, rather than half of it spinning
      // for ever on an answer that is not coming.
      .catch(() => {})
      .finally(() => {
        setLoading(false)
        setSettled(true)
      })
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

  // null = no OU filter at all. Selecting nothing while scoped means nothing matches, which is the
  // honest reading of "only the selected units" — silently showing everything would hide the state.
  const ouScope = useMemo(
    () => (ouScoped ? subtreeOf(groups, ouSelected) : null),
    [ouScoped, ouSelected, groups],
  )

  // Delete the OU you are scoped to and the list would go permanently empty with nothing
  // highlighted and nothing on screen explaining why. Prune the selection when the tree changes,
  // and fall back to unscoped once it is empty rather than leaving a filter that matches nobody.
  useEffect(() => {
    if (groups.length === 0) return // not loaded yet; pruning here would clear a valid selection
    setOuSelected((sel) => {
      const kept = sel.filter((id) => groups.some((g) => g.id === id))
      if (kept.length === sel.length) return sel
      if (kept.length === 0) setOuScoped(false)
      return kept
    })
  }, [groups])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return (data?.users || []).filter((u) => {
      if (roleFilter && u.role !== roleFilter) return false
      if (ouScope) {
        // An account with no primary group belongs to the Default OU by inheritance, so the tree
        // has to match it the same way the rest of the product resolves it.
        const own = u.primary_group || defaultGroup?.id || 0
        if (!ouScope.has(own)) return false
      }
      if (q && ![u.username, u.display_name, u.email].some((v) => (v || '').toLowerCase().includes(q))) return false
      return true
    })
  }, [data, search, roleFilter, ouScope, defaultGroup])

  // Cut an account's identity-provider binding without deleting the account. A link outlives the
  // person's IdP account, and while it stands whoever the IdP later issues that subject to would
  // sign in as this one. The account returns to local and its sessions end.
  const unlink = async (name: string) => {
    try {
      await api.del(`/api/admin/users/${encodeURIComponent(name)}/identity`)
      message.success(t('users.unlinked'))
      load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

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
    if (u === 'new') form.setFieldsValue({ username: '', display_name: '', email: '', role: 'user', primary_group: undefined, password: '', expires_at: null })
    else
      form.setFieldsValue({
        username: u.username,
        display_name: u.display_name,
        email: u.email,
        role: u.role,
        primary_group: u.primary_group || undefined,
        password: '',
        expires_at: u.expires_at ? dayjs(u.expires_at) : null,
      })
  }
  const saveEdit = async () => {
    const v = await form.validateFields()
    const primaryGroup = v.primary_group ?? 0
    const expiresAt = v.expires_at ? v.expires_at.format('YYYY-MM-DD') : ''
    try {
      if (editUser === 'new') {
        await api.post('/api/admin/users', {
          username: v.username,
          password: v.password,
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          primary_group: primaryGroup,
          expires_at: expiresAt,
        })
      } else {
        await patch((editUser as UserRow).username, {
          role: v.role,
          display_name: v.display_name || '',
          email: v.email || '',
          primary_group: primaryGroup,
          expires_at: expiresAt,
          ...(v.password ? { password: v.password } : {}),
        })
      }
      setEditUser(null)
      message.success(t('common.saved'))
      load()
    } catch (e) {
      message.error(errText(e, t))
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
              {/* A federated account has no local password, so knowing which rows those are is the
                  difference between offering a password reset and offering to revoke a binding. */}
              {u.federated && (
                <Popconfirm
                  title={t('users.unlinkConfirm', { slug: u.sso_slug || 'SSO' })}
                  okText={t('users.unlink')}
                  cancelText={t('common.cancel')}
                  onConfirm={() => unlink(u.username)}
                >
                  <Tag color="geekblue" style={{ cursor: 'pointer' }}>
                    {t('users.federatedTag', { slug: u.sso_slug || 'SSO' })}
                  </Tag>
                </Popconfirm>
              )}
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
      title: t('users.expires'),
      dataIndex: 'expires_at',
      width: 130,
      render: (v: string) => {
        if (!v) return <Typography.Text type="secondary">{t('users.expiresNever')}</Typography.Text>
        const expired = dayjs(v).endOf('day').isBefore(dayjs())
        return (
          <Space size={4}>
            <Typography.Text style={{ fontSize: 12 }} type={expired ? 'danger' : undefined}>
              {v}
            </Typography.Text>
            {expired && <Tag color="red">{t('users.expired')}</Tag>}
          </Space>
        )
      },
    },
    {
      // Activity, not sign-in. "Signed in Monday and used it all week" and "signed in Monday and
      // never came back" are the two answers this column is consulted to tell apart, and showing
      // the login time made them identical. The login time is still a fact worth having, so it is
      // the tooltip rather than gone.
      title: t('users.lastSeen'),
      dataIndex: 'last_seen',
      width: 160,
      render: (v: string, u) =>
        v ? (
          <Tooltip title={t('users.lastLoginWas', { at: u.last_login || t('users.never') })}>
            <Typography.Text style={{ fontSize: 12 }}>{v}</Typography.Text>
          </Tooltip>
        ) : (
          <Typography.Text type="secondary">{t('users.never')}</Typography.Text>
        ),
    },
    {
      title: '',
      width: 120,
      align: 'right',
      render: (_, u) => (
        <Space>
          <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(u)} aria-label={t('common.edit')} />
          <Button size="small" icon={<KeyOutlined />} onClick={() => setPwUser(u.username)} aria-label={t('users.newPassword')} />
          <Popconfirm title={t('common.deleteConfirm')} onConfirm={() => remove(u.username)} disabled={u.username === data?.me}>
            <Button size="small" danger icon={<DeleteOutlined />} disabled={u.username === data?.me} aria-label={t('common.delete')} />
          </Popconfirm>
        </Space>
      ),
    },
  ]

  const remove = async (name: string) => {
    await api.del(`/api/admin/users/${encodeURIComponent(name)}`)
    load()
  }

  const accountList = (
    <Space direction="vertical" size={16} style={{ width: '100%', minWidth: 0 }}>
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
        destroyOnHidden
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
          <Form.Item name="password" label={editUser === 'new' ? t('users.password') : t('users.newPassword')} rules={[{ required: editUser === 'new', min: 12, message: t('reset.tooShort') }]}>
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
          <Form.Item name="expires_at" label={t('users.expires')} extra={t('users.expiresHint')}>
            <DatePicker style={{ width: '100%' }} placeholder={t('users.expiresNever')} />
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
        destroyOnHidden
      >
        <Form form={pwForm} layout="vertical">
          <Form.Item name="password" label={t('users.newPassword')} rules={[{ required: true, min: 12, message: t('reset.tooShort') }]}>
            <Input.Password autoComplete="new-password" />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  )

  // Tree beside the list, the way an admin console scopes a directory: the OU hierarchy is visible
  // at all times, so inheritance stops being something you can only infer from an edit dialog.
  const accounts = (
    <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
      <OrgUnitPicker
        groups={groups}
        loading={!settled}
        unassigned={(data?.users || []).filter((u) => !u.primary_group).length}
        scoped={ouScoped}
        onScopedChange={setOuScoped}
        selected={ouSelected}
        onSelect={setOuSelected}
        onManage={() => setTab('groups')}
      />
      {accountList}
    </div>
  )

  return (
    <Tabs
      activeKey={tab}
      onChange={setTab}
      items={[
        { key: 'accounts', label: t('users.tabAccounts'), children: accounts },
        { key: 'groups', label: t('users.tabGroups'), children: <GroupsPanel groups={groups} groupsLoading={!settled} onChanged={load} /> },
      ]}
    />
  )
}

// Groups list + editor, shown as the "groups" sub-tab of the users page. A group's
// weight / urgent / priority drive the run queue for its members (group model B): a
// non-default group either overrides a field or inherits it from the Default group,
// which every unassigned user falls back to.
function GroupsPanel({ groups, groupsLoading, onChanged }: { groups: UserGroupRow[]; groupsLoading: boolean; onChanged: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  // Which OU the detail pane is showing. Held as an id, not the row: the list is refetched after
  // every save, so a held object would go stale the moment it was edited.
  const [selId, setSelId] = useState<number | null>(null)
  const [adding, setAdding] = useState(false)
  const [newName, setNewName] = useState('')
  const [newParent, setNewParent] = useState(0)
  // The urgent lane + ticket config is GLOBAL (not per-group), but it belongs with the
  // per-group weights conceptually, so it lives here rather than on the run-queue
  // settings page. Loaded from / saved to the shared batch config.
  const [urgentEnabled, setUrgentEnabled] = useState(false)
  const [reservedSlots, setReservedSlots] = useState(1)
  const [ticketPeriod, setTicketPeriod] = useState(7)
  const [maxJobs, setMaxJobs] = useState(1)
  const [cfgReady, setCfgReady] = useState(false)
  const [cfgErr, setCfgErr] = useState('')

  const loadCfg = () => {
    setCfgErr('')
    return api.get<BatchConfig>('/api/admin/batch/config').then(
      (r) => {
        setUrgentEnabled(!!r.urgent_enabled)
        setReservedSlots(r.reserved_slots)
        setTicketPeriod(r.ticket_period_days)
        setMaxJobs(r.max_jobs)
        setCfgReady(true)
      },
      // The controls were already disabled until this landed, but a disabled switch still shows a
      // position, and OFF here means "the urgent lane is hidden everywhere" — a description of the
      // deployment written by this file's useState. Group editing below stays usable either way.
      (e) => setCfgErr(errText(e, t)),
    )
  }
  useEffect(() => {
    loadCfg()
    // eslint-disable-next-line react-hooks/exhaustive-deps
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
      message.error(errText(e, t))
    }
  }

  const sel = groups.find((g) => g.id === selId) ?? null
  // Default to the root so the pane is never empty on arrival — an admin opening this tab wants to
  // see something, and the Default OU is the one every other setting falls back to.
  useEffect(() => {
    if (selId == null && groups.length > 0) setSelId((groups.find((g) => g.is_default) ?? groups[0]).id)
  }, [groups, selId])

  const createOU = async () => {
    const name = newName.trim()
    if (!name) return
    try {
      await api.post('/api/admin/groups', { name, description: '', parent_id: newParent })
      setAdding(false)
      setNewName('')
      setNewParent(0)
      onChanged()
    } catch (e) {
      message.error(errText(e, t))
    }
  }


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
        <LoadGate loading={!cfgReady && !cfgErr} error={cfgReady ? undefined : cfgErr} onRetry={loadCfg} minHeight={140}>
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
        </LoadGate>
      </Card>

      {/* Pick one on the left, see all of it on the right. A flat list of every OU plus a modal of
          ten fields plus a second modal for the allow-list was the thing that made this
          unreadable — and it hid the hierarchy, which is what explains every inherited value. */}
      <div style={{ display: 'flex', gap: 16, alignItems: 'flex-start' }}>
        <OrgUnitPicker
          mode="manage"
          groups={groups}
          loading={groupsLoading}
          unassigned={0}
          scoped
          onScopedChange={() => {}}
          selected={sel ? [sel.id] : []}
          onSelect={(ids) => setSelId(ids[0] ?? null)}
          onManage={() => {}}
          onAdd={() => setAdding(true)}
        />
        {sel ? (
          <OrgUnitDetail
            key={sel.id}
            group={sel}
            groups={groups}
            onChanged={onChanged}
            onDeleted={() => {
              setSelId(null)
              onChanged()
            }}
          />
        ) : (
          <Empty style={{ flex: 1, paddingTop: 48 }} description={t('ou.pickOne')} />
        )}
      </div>

      <Modal
        open={adding}
        title={t('ou.add')}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        onCancel={() => setAdding(false)}
        onOk={createOU}
        destroyOnHidden
      >
        {/* Creation asks only for a name and a place. Everything else has a sensible inherited
            value the moment it exists, and is changed in the pane where it can be seen. */}
        <Typography.Text strong>{t('users.groupName')}</Typography.Text>
        <Input autoFocus value={newName} onChange={(e) => setNewName(e.target.value)} style={{ marginBottom: 12 }} />
        <Typography.Text strong>{t('users.parentOu')}</Typography.Text>
        <Select
          style={{ width: '100%' }}
          value={newParent}
          onChange={setNewParent}
          options={[
            { value: 0, label: t('users.parentOuNone') },
            ...groups.map((g) => ({ value: g.id, label: g.name })),
          ]}
        />
      </Modal>
    </Space>
  )
}
