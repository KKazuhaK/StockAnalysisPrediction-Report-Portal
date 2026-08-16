import { useEffect, useMemo, useState } from 'react'
import { Alert, App, Button, Input, Select, Space, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { Role, SSOProviderAdmin, SSORuleRow, SSORulesResp, UserGroupRow } from '../../api/types'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'
import LoadGate from '../../components/LoadGate'
import StickyActionBar from '../../components/StickyActionBar'

// The group-rules editor.
//
// These rules decide the role AND the organizational unit of a federated account, and in this portal
// the OU is what carries report visibility, the run allow-list and the daily quota — so this table is
// where a federated user's real permissions are set. The engine shipped with tests and no UI, which
// meant the rule list was always empty and every SSO login silently fell through to the provider
// defaults.
//
// Order is the contract: first match wins. That is why the whole list is saved as one array (the
// array IS the order) rather than row by row, and why rows drag rather than carry a number field.

const emptyRule = (): SSORuleRow => ({
  id: 0,
  provider_id: 0,
  ord: 0,
  enabled: true,
  attr: 'groups',
  value: '',
  target_role: '',
  target_group: 0,
  keep_on_miss: false,
  ci: true,
  note: '',
})

export default function SSORulesEditor({
  providers,
  groups,
  roles,
}: {
  providers: SSOProviderAdmin[]
  groups: UserGroupRow[]
  roles: Role[]
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [rules, setRules] = useState<SSORuleRow[]>([])
  const [shadowed, setShadowed] = useState<number[]>([])
  // true from the start: save() PUTs the whole rule list, so a Save reached before the GET landed
  // — or after it silently failed — would write "no rules" over every mapping this portal has.
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [saving, setSaving] = useState(false)
  const [dirty, setDirty] = useState(false)

  const load = () => {
    setLoading(true)
    setLoadErr('')
    api
      .get<SSORulesResp>('/api/admin/sso/rules')
      .then((r) => {
        setRules(r.rules || [])
        setShadowed(r.shadowed || [])
        setDirty(false)
        setLoaded(true)
      })
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }
  useEffect(load, [])

  // Row keys are positions, not ids: an unsaved row has no id yet, and the server assigns ids from
  // the array order on save, so a position is the only thing stable across an edit.
  const keys = useMemo(() => rules.map((_, i) => String(i)), [rules])

  const patch = (i: number, v: Partial<SSORuleRow>) => {
    setRules((prev) => prev.map((r, j) => (i === j ? { ...r, ...v } : r)))
    setDirty(true)
  }

  const save = async () => {
    setSaving(true)
    try {
      await api.put('/api/admin/sso/rules', { rules })
      message.success(t('common.saved'))
      load()
    } catch (e) {
      message.error(String(e))
    } finally {
      setSaving(false)
    }
  }

  const providerOptions = [
    { value: 0, label: t('sso.rules.anyProvider') },
    ...providers.filter((p) => p.id > 0).map((p) => ({ value: p.id, label: p.name || p.slug || p.kind })),
  ]

  const columns = [
    {
      title: '',
      width: 40,
      render: () => <DragHandle />,
    },
    {
      title: t('sso.rules.enabled'),
      width: 80,
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Switch size="small" checked={rules[i].enabled} onChange={(v) => patch(i, { enabled: v })} />
      ),
    },
    {
      title: t('sso.rules.provider'),
      width: 150,
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Select
          size="small"
          style={{ width: '100%' }}
          value={rules[i].provider_id}
          options={providerOptions}
          onChange={(v) => patch(i, { provider_id: v })}
        />
      ),
    },
    {
      title: t('sso.rules.attr'),
      width: 140,
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Input
          size="small"
          value={rules[i].attr}
          placeholder="groups"
          onChange={(e) => patch(i, { attr: e.target.value })}
        />
      ),
    },
    {
      title: t('sso.rules.value'),
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Space.Compact style={{ width: '100%' }}>
          <Input size="small" value={rules[i].value} onChange={(e) => patch(i, { value: e.target.value })} />
          <Tooltip title={t('sso.rules.ciHint')}>
            <Button
              size="small"
              type={rules[i].ci ? 'primary' : 'default'}
              onClick={() => patch(i, { ci: !rules[i].ci })}
            >
              Aa
            </Button>
          </Tooltip>
        </Space.Compact>
      ),
    },
    {
      title: t('sso.rules.targetRole'),
      width: 130,
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Select
          size="small"
          allowClear
          style={{ width: '100%' }}
          placeholder={t('sso.rules.unchanged')}
          value={rules[i].target_role || undefined}
          options={roles.map((r) => ({ value: r.code, label: r.name }))}
          onChange={(v) => patch(i, { target_role: v || '' })}
        />
      ),
    },
    {
      title: t('sso.rules.targetGroup'),
      width: 160,
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Select
          size="small"
          allowClear
          style={{ width: '100%' }}
          placeholder={t('sso.rules.unchanged')}
          value={rules[i].target_group || undefined}
          options={groups.map((g) => ({ value: g.id, label: g.name }))}
          onChange={(v) => patch(i, { target_group: v || 0 })}
        />
      ),
    },
    {
      title: (
        <Tooltip title={t('sso.rules.keepOnMissHint')}>
          <span>{t('sso.rules.keepOnMiss')}</span>
        </Tooltip>
      ),
      width: 100,
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Switch size="small" checked={rules[i].keep_on_miss} onChange={(v) => patch(i, { keep_on_miss: v })} />
      ),
    },
    {
      title: '',
      width: 120,
      render: (_: unknown, _r: SSORuleRow, i: number) => (
        <Space size={4}>
          {shadowed.includes(rules[i].id) && !dirty && (
            <Tooltip title={t('sso.rules.shadowedHint')}>
              <Tag color="warning">{t('sso.rules.shadowed')}</Tag>
            </Tooltip>
          )}
          <Button
            size="small"
            type="text"
            danger
            icon={<DeleteOutlined />}
            onClick={() => {
              setRules((prev) => prev.filter((_, j) => j !== i))
              setDirty(true)
            }}
          />
        </Space>
      ),
    },
  ]

  return (
    <div>
      <LoadGate loading={loading && !loaded} error={loaded ? undefined : loadErr} onRetry={load} minHeight={220}>
      <Alert type="info" showIcon style={{ marginBottom: 12 }} message={t('sso.rules.intro')} />
      {providers.filter((p) => p.id > 0).length === 0 && (
        <Alert type="warning" showIcon style={{ marginBottom: 12 }} message={t('sso.rules.noProviders')} />
      )}

      <SortableWrapper
        ids={keys}
        onReorder={(order) => {
          setRules(order.map((k) => rules[Number(k)]))
          setDirty(true)
        }}
      >
        <Table<SSORuleRow>
          rowKey={(_, i) => String(i)}
          size="small"
          loading={loading}
          dataSource={rules}
          pagination={false}
          components={sortableTableComponents}
          columns={columns}
          // Eight columns of editable controls want ~1100px. A phone squeezes them to the width of
          // their own borders rather than admitting it; scrolling sideways at least leaves each
          // select and input usable.
          scroll={{ x: 1100 }}
          locale={{ emptyText: t('sso.rules.empty') }}
        />
      </SortableWrapper>

      <Space style={{ marginTop: 12 }}>
        <Button
          icon={<PlusOutlined />}
          onClick={() => {
            setRules((prev) => [...prev, emptyRule()])
            setDirty(true)
          }}
        >
          {t('sso.rules.add')}
        </Button>
        <Typography.Text type="secondary">{t('sso.rules.orderHint')}</Typography.Text>
      </Space>

      {dirty && (
        <StickyActionBar>
          <Button onClick={load}>{t('common.cancel')}</Button>
          <Button type="primary" loading={saving} onClick={save}>
            {t('common.save')}
          </Button>
        </StickyActionBar>
      )}
      </LoadGate>
    </div>
  )
}
