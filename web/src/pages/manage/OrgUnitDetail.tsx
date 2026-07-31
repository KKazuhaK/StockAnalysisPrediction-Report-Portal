import { useEffect, useState } from 'react'
import { App, Breadcrumb, Button, Card, Empty, Input, InputNumber, Popconfirm, Select, Space, Switch, Tag, Typography } from 'antd'
import { DeleteOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { GroupTargetsResp, UserGroupRow } from '../../api/types'
import InheritField from './InheritField'
import { ouPath, resolveOU, type UrgentPolicy } from './ouSettings'

const RUN_SURFACES = ['run', 'batch', 'recurring', 'chat'] as const

// Everything about ONE organizational unit, beside the tree that selects it.
//
// It replaces a flat list of every OU plus a modal holding ten fields plus a second modal holding
// the workflow allow-list. The complaint was that it was all in one place and none of it was
// legible; the answer is the shape an admin console usually has — pick one on the left, see all of
// it on the right, in sections that separate what an OU IS from what its members may DO.

interface Draft {
  name: string
  description: string
  parent_id: number
  urgentInherit: boolean
  urgentPolicy: UrgentPolicy
  weight: number
  maxqInherit: boolean
  maxQueued: number
  windowInherit: boolean
  runWindow: string
  priority: number | null
  restricted: boolean
  quotaInherit: boolean
  dailyQuota: number
}

function draftOf(g: UserGroupRow, def?: UserGroupRow): Draft {
  const r = resolveOU(g, def)
  const isDefault = !!g.is_default
  return {
    name: g.name,
    description: g.description ?? '',
    parent_id: g.parent_id ?? 0,
    urgentInherit: !isDefault && g.allow_urgent == null && g.urgent_unlimited == null && g.weight == null,
    urgentPolicy: r.urgent.value,
    weight: r.weight.value,
    maxqInherit: !isDefault && g.max_queued == null,
    maxQueued: r.maxQueued.value,
    windowInherit: !isDefault && g.run_window == null,
    runWindow: r.runWindow.value,
    priority: r.priority.value,
    restricted: !!g.restricted,
    quotaInherit: !isDefault && g.daily_run_quota == null,
    dailyQuota: r.dailyQuota.value,
  }
}

export default function OrgUnitDetail({
  group,
  groups,
  onChanged,
  onDeleted,
}: {
  group: UserGroupRow
  groups: UserGroupRow[]
  onChanged: () => void
  onDeleted: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const def = groups.find((g) => g.is_default)
  const isDefault = !!group.is_default
  // What this OU WOULD inherit — resolved as if it set nothing itself. The inherit option has to
  // name that, not the OU's current value: an OU overriding max-queued to 9 was offering "inherit
  // Default — 9", which is the override it would be replacing.
  const wouldInherit = resolveOU(
    {
      ...group,
      allow_urgent: null,
      urgent_unlimited: null,
      weight: null,
      max_queued: null,
      run_window: null,
      daily_run_quota: null,
      priority: '',
    } as UserGroupRow,
    def,
  )

  const [d, setD] = useState<Draft>(() => draftOf(group, def))
  const [saving, setSaving] = useState(false)
  // Re-seed when the tree selection moves, or the pane would keep the previous OU's draft.
  useEffect(() => setD(draftOf(group, def)), [group, def])
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((p) => ({ ...p, [k]: v }))

  // Who the inherited values come from. Run governance falls back to the Default group; the OU tree
  // is not involved, which is a distinction the old screen never drew and this label has to.
  const from = def?.name ?? t('users.defaultGroupTag')
  const num = (n: number) => (n > 0 ? String(n) : t('ou.unlimited'))

  const save = async () => {
    setSaving(true)
    try {
      await api.put(`/api/admin/groups/${group.id}`, {
        name: d.name,
        description: d.description,
        // The urgent policy is one control mapped back onto three stored fields, so "allowed" and
        // "unlimited" can never contradict each other the way the tags used to.
        allow_urgent: d.urgentInherit ? null : d.urgentPolicy !== 'off',
        urgent_unlimited: d.urgentInherit ? null : d.urgentPolicy === 'unlimited',
        weight: d.urgentInherit ? null : d.urgentPolicy === 'ticket' ? d.weight : 0,
        max_queued: d.maxqInherit ? null : d.maxQueued,
        run_window: d.windowInherit ? null : d.runWindow,
        priority: d.priority == null ? '' : String(d.priority),
        restricted: isDefault ? false : d.restricted,
        daily_run_quota: d.quotaInherit ? null : d.dailyQuota,
        ...(isDefault ? {} : { parent_id: d.parent_id }),
      })
      message.success(t('common.saved'))
      onChanged()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    try {
      await api.del(`/api/admin/groups/${group.id}`)
      message.success(t('common.saved'))
      onDeleted()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  return (
    <Space direction="vertical" size={16} style={{ flex: 1, minWidth: 0, maxWidth: 760 }}>
      <Card
        size="small"
        title={
          <Space>
            <span>{group.name}</span>
            {isDefault && <Tag color="green">{t('users.defaultGroupTag')}</Tag>}
            {group.restricted_effective && (
              <Tag color="volcano">
                {t('users.restrictedTag')}
                {!group.restricted && <span style={{ opacity: 0.6 }}> · {t('users.inheritedTag')}</span>}
              </Tag>
            )}
          </Space>
        }
        extra={
          <Button type="primary" size="small" loading={saving} onClick={save}>
            {t('common.save')}
          </Button>
        }
      >
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('ou.path')}
        </Typography.Text>
        <Breadcrumb style={{ marginBottom: 12 }} items={ouPath(groups, group.id).map((n) => ({ title: n }))} />

        <Typography.Text strong>{t('users.groupName')}</Typography.Text>
        <Input value={d.name} onChange={(e) => set('name', e.target.value)} style={{ marginBottom: 12 }} />
        <Typography.Text strong>{t('users.groupDesc')}</Typography.Text>
        <Input.TextArea
          rows={2}
          value={d.description}
          onChange={(e) => set('description', e.target.value)}
          style={{ marginBottom: 12 }}
        />
        {!isDefault && (
          <>
            <Typography.Text strong>{t('users.parentOu')}</Typography.Text>
            <Select
              showSearch
              optionFilterProp="label"
              style={{ width: '100%' }}
              value={d.parent_id}
              onChange={(v) => set('parent_id', v)}
              options={[
                { value: 0, label: t('users.parentOuNone') },
                // An OU cannot be placed under itself. Descendants are refused server-side too,
                // but not offering them keeps the admin out of a dead end.
                ...groups.filter((g) => g.id !== group.id).map((g) => ({ value: g.id, label: g.name })),
              ]}
            />
            <Typography.Text type="secondary" style={{ display: 'block', fontSize: 12, marginTop: 4 }}>
              {t('users.parentOuHint')}
            </Typography.Text>
          </>
        )}
      </Card>

      <Card size="small" title={t('ou.sectionRuns')}>
        <InheritField
          label={t('users.urgentPolicy')}
          hint={t('users.urgentPolicyHint')}
          from={from}
          inherited={t(`ou.urgent.${wouldInherit.urgent.value}`)}
          inheriting={d.urgentInherit}
          onInheritingChange={(v) => set('urgentInherit', v)}
        >
          <Space>
            <Select
              size="small"
              style={{ width: 150 }}
              value={d.urgentPolicy}
              onChange={(v) => set('urgentPolicy', v)}
              options={[
                { value: 'off', label: t('users.urgentOff') },
                { value: 'ticket', label: t('users.urgentTicket') },
                { value: 'unlimited', label: t('users.urgentUnlimitedOpt') },
              ]}
            />
            {d.urgentPolicy === 'ticket' && (
              <InputNumber size="small" min={0} max={999} value={d.weight} onChange={(v) => set('weight', v ?? 0)} />
            )}
          </Space>
        </InheritField>

        <InheritField
          label={t('users.maxQueued')}
          hint={t('users.maxQueuedHint')}
          from={from}
          inherited={num(wouldInherit.maxQueued.value)}
          inheriting={d.maxqInherit}
          onInheritingChange={(v) => set('maxqInherit', v)}
        >
          <InputNumber size="small" min={0} max={999} value={d.maxQueued} onChange={(v) => set('maxQueued', v ?? 0)} />
        </InheritField>

        <InheritField
          label={t('users.runWindow')}
          hint={t('users.runWindowHint')}
          from={from}
          inherited={wouldInherit.runWindow.value || t('ou.anyHour')}
          inheriting={d.windowInherit}
          onInheritingChange={(v) => set('windowInherit', v)}
        >
          <Input size="small" style={{ width: 120 }} placeholder="9-18" value={d.runWindow} onChange={(e) => set('runWindow', e.target.value)} />
        </InheritField>

        {/* Priority falls back to the SYSTEM default, not to the Default group — a different
            fallback from everything above it, so it names a different source. */}
        <InheritField
          label={t('users.priority')}
          hint={t('users.priorityHint')}
          from={t('ou.systemDefault')}
          inherited={t('users.prioritySystemDefault')}
          inheriting={d.priority == null}
          onInheritingChange={(v) => set('priority', v ? null : 50)}
        >
          <InputNumber size="small" min={0} max={100} value={d.priority ?? undefined} onChange={(v) => set('priority', v ?? 0)} />
        </InheritField>
      </Card>

      {!isDefault && (
        <Card size="small" title={t('ou.sectionTenancy')}>
          <Space align="start" style={{ marginBottom: 12 }}>
            <Switch checked={d.restricted} onChange={(v) => set('restricted', v)} />
            <div>
              <Typography.Text strong style={{ display: 'block' }}>
                {t('users.restricted')}
              </Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('users.restrictedHint')}
              </Typography.Text>
            </div>
          </Space>
          <InheritField
            label={t('users.dailyRunQuota')}
            hint={t('users.dailyRunQuotaHint')}
            from={t('users.parentOu')}
            inherited={num(wouldInherit.dailyQuota.value)}
            inheriting={d.quotaInherit}
            onInheritingChange={(v) => set('quotaInherit', v)}
          >
            <InputNumber size="small" min={0} max={999} value={d.dailyQuota} onChange={(v) => set('dailyQuota', v ?? 0)} />
          </InheritField>
        </Card>
      )}

      {/* Only a restricted OU has an allow-list; for anyone else it governs nothing. It used to hide
          behind a small icon button opening a second modal. */}
      {group.restricted_effective && <TargetsSection group={group} />}

      {!isDefault && (
        <Popconfirm title={t('users.deleteGroupConfirm')} onConfirm={remove}>
          <Button danger icon={<DeleteOutlined />}>
            {t('ou.deleteOu')}
          </Button>
        </Popconfirm>
      )}
    </Space>
  )
}

// The "OU × workflow × surface" allow-list (ADR 0022 R3). A restricted OU is default-deny: no row
// here means its members can run nothing, which is what the empty state has to say.
function TargetsSection({ group }: { group: UserGroupRow }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [data, setData] = useState<GroupTargetsResp | null>(null)
  const [granted, setGranted] = useState<Record<number, string[]>>({})
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setData(null)
    api
      .get<GroupTargetsResp>(`/api/admin/groups/${group.id}/targets`)
      .then((r) => {
        setData(r)
        const m: Record<number, string[]> = {}
        for (const g of r.granted || []) m[g.target_id] = g.surfaces
        setGranted(m)
      })
      .catch((e) => message.error(errText(e, t)))
  }, [group.id])

  const save = async () => {
    setSaving(true)
    try {
      await api.put(`/api/admin/groups/${group.id}/targets`, {
        granted: Object.entries(granted).map(([id, surfaces]) => ({ target_id: Number(id), surfaces })),
      })
      message.success(t('common.saved'))
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card
      size="small"
      title={t('ou.sectionTargets')}
      extra={
        <Button size="small" loading={saving} onClick={save}>
          {t('common.save')}
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t('users.groupTargetsHint')}
      </Typography.Paragraph>
      {data && data.targets.length === 0 && <Empty description={t('users.groupTargetsNoTargets')} />}
      <Space direction="vertical" size={10} style={{ width: '100%' }}>
        {(data?.targets || []).map((tg) => {
          const on = granted[tg.id] != null
          return (
            <div key={tg.id}>
              <Space wrap>
                <Switch
                  size="small"
                  checked={on}
                  onChange={(v) =>
                    setGranted((g) => {
                      const next = { ...g }
                      if (v) next[tg.id] = tg.surfaces
                      else delete next[tg.id]
                      return next
                    })
                  }
                />
                <Typography.Text strong>{tg.name}</Typography.Text>
                {tg.output_subtype && <Tag color="blue">{tg.output_subtype}</Tag>}
              </Space>
              {on && (
                <Space wrap style={{ paddingLeft: 34, marginTop: 4 }}>
                  {RUN_SURFACES.filter((sf) => tg.surfaces.includes(sf)).map((sf) => (
                    <Tag.CheckableTag
                      key={sf}
                      checked={(granted[tg.id] || []).includes(sf)}
                      onChange={(v) =>
                        setGranted((g) => {
                          const cur = g[tg.id] || []
                          return { ...g, [tg.id]: v ? [...cur, sf] : cur.filter((x) => x !== sf) }
                        })
                      }
                    >
                      {t(`users.surface.${sf}`)}
                    </Tag.CheckableTag>
                  ))}
                </Space>
              )}
            </div>
          )
        })}
      </Space>
    </Card>
  )
}
