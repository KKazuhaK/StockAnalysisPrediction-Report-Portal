import { useEffect, useMemo, useState } from 'react'
import {
  Alert,
  App,
  Button,
  DatePicker,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Radio,
  Segmented,
  Select,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
  theme,
} from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, errText } from '../../api/client'
import { forgetTags } from '../../lib/conditionalGet'
import type {
  AdminAnnouncement,
  Announcement,
  AnnouncementAudience,
  AnnouncementLevel,
  AnnouncementScope,
} from '../../api/types'
import { AnnouncementAlert } from '../../components/SiteAnnouncement'
import type { Principal } from '../../api/types'
import { DragHandle, SortableItem, SortableWrapper } from './dnd'
import LoadGate from '../../components/LoadGate'

const { RangePicker } = DatePicker
const ANNOUNCEMENT_LEVELS: AnnouncementLevel[] = ['notice', 'success', 'warning', 'error']

const LEVEL_DOT: Record<AnnouncementLevel, string> = {
  notice: 'colorInfo',
  success: 'colorSuccess',
  warning: 'colorWarning',
  error: 'colorError',
}

// An enabled announcement with no end time that nobody has touched in this long gets an amber
// nudge in the list. It is the cheap 90% of the display window, for the operator who will not set
// one: an incident banner nobody remembers to take down is how a whole announcement band stops
// being read.
const STALE_DAYS = 14

// Above this many announcements live at once, the console says so. Not a limit — saving is never
// refused, because a refusal at the moment somebody most needs to broadcast does not actually stop
// readers from ignoring an overcrowded band. It counts what readers face (enabled and inside its
// window), not the draft pile, because that is the number that degrades the band.
const CROWDED = 5

interface FormValues {
  enabled: boolean
  popup: boolean
  dismissible: boolean
  level: AnnouncementLevel
  scope: AnnouncementScope
  audience: AnnouncementAudience
  grants: string[]
  title: string
  content: string
  window?: [dayjs.Dayjs | null, dayjs.Dayjs | null] | null
}

// Site announcements. One row per announcement, dragged into the order readers see, each saved on
// its own — there is no page-level Save button, the way the entry-buttons page works.
export default function AnnouncementPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { token } = theme.useToken()
  const [items, setItems] = useState<AdminAnnouncement[]>([])
  const [groups, setGroups] = useState<Principal[]>([])
  const [users, setUsers] = useState<Principal[]>([])
  const [usersTruncated, setUsersTruncated] = useState(false)
  const [collapse, setCollapse] = useState(false)
  const [previewAs, setPreviewAs] = useState<string | undefined>()
  const [preview, setPreview] = useState<Announcement[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [editing, setEditing] = useState<AdminAnnouncement | null>(null)
  const [open, setOpen] = useState(false)
  const [saving, setSaving] = useState(false)
  const [form] = Form.useForm<FormValues>()

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<{
        items: AdminAnnouncement[]
        groups: Principal[]
        users: Principal[]
        usersTruncated: boolean
        collapse: boolean
      }>('/api/admin/announcements')
      .then((r) => {
        setItems(r.items || [])
        setCollapse(r.collapse === true)
        setGroups(r.groups || [])
        setUsers(r.users || [])
        setUsersTruncated(r.usersTruncated === true)
        setLoaded(true)
      })
      // Not a `finally`-only settle: a failed GET that renders as "no announcements yet" looks
      // exactly like a portal that has none, and the drag handler would then be able to send
      // "replace the whole order" from a list that was never there.
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // The admin's own shell polls the reader feed every 60s. After a change, drop its stored ETag so
  // the edit shows up in this tab now rather than up to a minute later.
  const refreshed = () => {
    forgetTags('/api/announcements')
    return load()
  }

  const ids = items.map((a) => String(a.id))

  const reorder = (orderedIds: string[]) => {
    const byId = new Map(items.map((a) => [String(a.id), a]))
    const next = orderedIds.map((k, i) => ({ ...(byId.get(k) as AdminAnnouncement), ord: i }))
    setItems(next) // optimistic; the server is the arbiter and a rejection reloads
    api
      .post('/api/admin/announcements/reorder', { ids: orderedIds.map(Number) })
      .then(() => forgetTags('/api/announcements'))
      // A 409 means somebody else changed the list while this page held an old copy. Reloading is
      // the honest response — better a list that visibly refreshes than an order that silently
      // reverts on the next visit.
      .catch((e) => {
        message.error(errText(e, t))
        load()
      })
  }

  const openEdit = (a: AdminAnnouncement | null) => {
    setEditing(a)
    form.setFieldsValue({
      enabled: a ? a.enabled : true,
      popup: a ? a.popup : false,
      dismissible: a ? a.dismissible : false,
      level: a?.level || 'notice',
      scope: a?.scope || 'home',
      audience: a?.audience || 'all',
      grants: a?.grants || [],
      title: a?.title || '',
      content: a?.content || '',
      window: a && (a.startsAt || a.endsAt) ? [a.startsAt ? dayjs(a.startsAt) : null, a.endsAt ? dayjs(a.endsAt) : null] : null,
    })
    setOpen(true)
  }

  const submit = async () => {
    const v = await form.validateFields()
    const [from, to] = v.window ?? [null, null]
    const payload = {
      enabled: v.enabled === true,
      popup: v.popup === true,
      dismissible: v.dismissible === true,
      level: v.level || 'notice',
      scope: v.scope || 'home',
      audience: v.audience || 'all',
      // Always sent from this form, which holds the whole row. The field is optional on the wire
      // precisely so a partial writer can leave the audience alone; this is not one.
      grants: v.audience === 'grant' ? v.grants || [] : [],
      title: v.title || '',
      content: v.content || '',
      startsAt: from ? from.toISOString() : '',
      endsAt: to ? to.toISOString() : '',
      // Sent so the server can refuse a save built on a copy somebody else has already replaced.
      ...(editing ? { updatedAt: editing.updatedAt } : {}),
    }
    setSaving(true)
    try {
      if (editing) await api.put(`/api/admin/announcements/${editing.id}`, payload)
      else await api.post('/api/admin/announcements', payload)
      setOpen(false)
      message.success(t('common.saved'))
      await refreshed()
    } finally {
      setSaving(false)
    }
  }

  // The inline switches PATCH one field. Re-sending the whole row to flip one would write back the
  // title and body this page loaded, silently reverting another operator's edit.
  const toggle = async (a: AdminAnnouncement, field: 'enabled' | 'popup', on: boolean) => {
    setItems((prev) => prev.map((x) => (x.id === a.id ? { ...x, [field]: on } : x)))
    try {
      await api.patch(`/api/admin/announcements/${a.id}`, { [field]: on })
      forgetTags('/api/announcements')
    } catch (e) {
      message.error(errText(e, t))
      load()
    }
  }

  // One site-wide display switch, saved on its own the moment it is flipped — the settings endpoint
  // merges per field, so posting this alone cannot disturb the branding beside it.
  const saveCollapse = async (on: boolean) => {
    setCollapse(on) // optimistic; a failure puts it back
    try {
      await api.post('/api/admin/settings', { announcementCollapse: on })
      forgetTags('/api/announcements')
    } catch (e) {
      setCollapse(!on)
      message.error(errText(e, t))
    }
  }

  const remove = async (id: number) => {
    await api.del(`/api/admin/announcements/${id}`)
    await refreshed()
  }

  // Only the first popup-eligible announcement actually interrupts a reader, so the switches below
  // it are decorative this page load. Saying so on the row is the point: an ignored setting should
  // be visible where it is set, not discovered by an operator wondering why nothing popped up.
  const firstPopupId = items.find((a) => a.popup && a.enabled)?.id

  // Two groups, the way the report-versions picker does it, so an admin who has used one knows
  // this one. The value IS the principal string, which is what the server stores.
  const principalOptions = [
    { label: t('announcementAdmin.principalGroups'), options: groups.map((g) => ({
      value: g.principal,
      label: g.restricted ? `${g.name} · ${t('announcementAdmin.restrictedTag')}` : g.name,
    })) },
    { label: t('announcementAdmin.principalUsers'), options: users.map((u) => ({
      value: u.principal,
      label: u.display && u.display !== u.name ? `${u.display} (${u.name})` : u.name,
    })) },
  ]
  const principalName = (p: string) =>
    groups.find((g) => g.principal === p)?.name ?? users.find((u) => u.principal === p)?.name ?? p

  const loadPreview = (principal?: string) => {
    setPreviewAs(principal)
    setPreview(null)
    if (!principal) return
    api
      .get<{ items: Announcement[] }>(`/api/admin/announcements/preview?principal=${encodeURIComponent(principal)}`)
      .then((r) => setPreview(r.items || []))
      .catch((e) => {
        message.error(errText(e, t))
        setPreviewAs(undefined)
      })
  }

  // A targeted announcement with no recipients left reaches nobody and says nothing about it. The
  // save path refuses to create one, but deleting the group it named produces the same state after
  // the fact — so it has to be a status the list reports, not only a validation rule.
  const unreachable = (a: AdminAnnouncement) => a.audience === 'grant' && a.grants.length === 0

  const statusOf = (a: AdminAnnouncement) => {
    if (!a.enabled) return { key: 'draft', color: undefined }
    if (unreachable(a)) return { key: 'unreachable', color: 'red' }
    const now = Date.now()
    if (a.startsAt && Date.parse(a.startsAt) > now) return { key: 'scheduled', color: 'blue' }
    if (a.endsAt && Date.parse(a.endsAt) <= now) return { key: 'expired', color: undefined }
    return { key: 'live', color: 'green' }
  }

  const liveCount = items.filter((a) => statusOf(a).key === 'live').length

  const staleDays = (a: AdminAnnouncement) => {
    if (!a.enabled || a.endsAt || !a.updatedAt) return 0
    const days = Math.floor((Date.now() - Date.parse(a.updatedAt)) / 86400000)
    return Number.isNaN(days) || days < STALE_DAYS ? 0 : days
  }

  const row = (a: AdminAnnouncement) => {
    const status = statusOf(a)
    const stale = staleDays(a)
    const preview = (a.content || '').split('\n')[0]
    return (
      <div
        style={{
          display: 'flex',
          alignItems: 'center',
          gap: 8,
          flexWrap: 'wrap',
          padding: '8px 10px',
          borderRadius: 8,
          background: token.colorFillQuaternary,
          border: `1px solid ${token.colorBorderSecondary}`,
          opacity: a.enabled ? 1 : 0.55, // a switched-off row reads as off at a glance
        }}
      >
        <DragHandle />
        <span
          aria-hidden
          style={{
            width: 8,
            height: 8,
            borderRadius: 4,
            flexShrink: 0,
            background: (token as unknown as Record<string, string>)[LEVEL_DOT[a.level]],
          }}
        />
        <Typography.Text strong={!!a.title} type={a.title ? undefined : 'secondary'}>
          {a.title || t('announcementAdmin.untitled')}
        </Typography.Text>
        <Typography.Text type="secondary" ellipsis style={{ flex: 1, minWidth: 80, fontSize: 12 }}>
          {preview}
        </Typography.Text>
        {status.key === 'unreachable' ? (
          <Tooltip title={t('announcementAdmin.unreachableHint')}>
            <Tag color="red">{t('announcementAdmin.status.unreachable')}</Tag>
          </Tooltip>
        ) : (
          <Tag color={status.color}>{t(`announcementAdmin.status.${status.key}`)}</Tag>
        )}
        {a.scope === 'app' && <Tag color="geekblue">{t('announcementAdmin.scope.app')}</Tag>}
        {a.audience === 'grant' && (
          <Tooltip title={a.grants.map(principalName).join('、')}>
            <Tag color="cyan">
              {a.grants.length === 1
                ? principalName(a.grants[0])
                : t('announcementAdmin.audienceCount', { count: a.grants.length })}
            </Tag>
          </Tooltip>
        )}
        {a.popup && a.enabled && a.id !== firstPopupId && (
          <Tooltip title={t('announcementAdmin.popupSkippedHint')}>
            <Tag>{t('announcementAdmin.popupSkipped')}</Tag>
          </Tooltip>
        )}
        {a.popup && <Tag color="purple">{t('announcementAdmin.popupTag')}</Tag>}
        {stale > 0 && (
          <Tooltip title={t('announcementAdmin.staleHint')}>
            <Tag color="orange">{t('announcementAdmin.stale', { count: stale })}</Tag>
          </Tooltip>
        )}
        <Tooltip title={t('announcementAdmin.enabled')}>
          <Switch
            size="small"
            aria-label={t('announcementAdmin.enabled')}
            checked={a.enabled}
            onChange={(v) => toggle(a, 'enabled', v)}
          />
        </Tooltip>
        <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(a)} />
        <Popconfirm title={t('announcementAdmin.deleteConfirm')} onConfirm={() => remove(a.id)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      </div>
    )
  }

  // The editor's live rendering of the row being edited. Named apart from `preview`, which is the
  // "what would this OU see" panel above the list — two different questions.
  const livePreview = useMemo(
    () => (
      <Form.Item shouldUpdate noStyle>
        {({ getFieldValue }) => {
          const title = String(getFieldValue('title') || '').trim()
          const content = String(getFieldValue('content') || '').trim()
          if (!title && !content) return null
          return (
            <AnnouncementAlert
              announcement={{
                level: getFieldValue('level') || 'notice',
                title,
                content,
                dismissible: getFieldValue('dismissible') === true,
              }}
              style={{ marginTop: 4 }}
            />
          )
        }}
      </Form.Item>
    ),
    [],
  )

  return (
    <Space direction="vertical" size={12} style={{ width: '100%', maxWidth: 860 }}>
      <LoadGate loading={loading} error={loadErr} onRetry={load}>
        <Space direction="vertical" size={10} style={{ width: '100%' }}>
          <Space wrap>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => openEdit(null)}>
              {t('announcementAdmin.add')}
            </Button>
            {/* You do not receive your own targeted announcements — the audience filter applies to
                admins too — so "addressed correctly" and "addressed to nobody" look identical from
                here. This is the only cheap way to tell them apart before publishing, and it asks
                the server the same question a real reader's browser asks. */}
            <Select
              allowClear
              showSearch
              optionFilterProp="label"
              style={{ minWidth: 200 }}
              placeholder={t('announcementAdmin.previewAs')}
              value={previewAs}
              onChange={loadPreview}
              options={principalOptions}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('announcementAdmin.hint')}
            </Typography.Text>
          </Space>

          <Space size={8} align="center" wrap>
            <Switch
              size="small"
              aria-label={t('announcementAdmin.collapse')}
              checked={collapse}
              onChange={saveCollapse}
            />
            <Typography.Text style={{ fontSize: 13 }}>{t('announcementAdmin.collapse')}</Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('announcementAdmin.collapseHint')}
            </Typography.Text>
          </Space>

          {previewAs && (
            <div
              style={{
                border: `1px dashed ${token.colorBorder}`,
                borderRadius: 8,
                padding: 12,
                background: token.colorFillQuaternary,
              }}
            >
              <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginBottom: 8 }}>
                {t('announcementAdmin.previewFor', { name: principalName(previewAs) })}
              </Typography.Text>
              {preview === null ? (
                <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
              ) : preview.length === 0 ? (
                <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('announcementAdmin.previewEmpty')} />
              ) : (
                <Space direction="vertical" size={8} style={{ width: '100%' }}>
                  {preview.map((a) => (
                    <AnnouncementAlert key={a.id} announcement={a} />
                  ))}
                </Space>
              )}
            </div>
          )}

          {liveCount > CROWDED && (
            <Alert
              showIcon
              type="warning"
              message={t('announcementAdmin.crowded', { count: liveCount })}
              style={{ borderRadius: 8 }}
            />
          )}

          {items.length === 0 ? (
            <Empty description={t('announcementAdmin.empty')} />
          ) : (
            // Dragging is disabled until the list has actually loaded: reorder replaces the whole
            // order in one call, and a page that never rendered has no order worth sending.
            <SortableWrapper ids={loaded ? ids : []} onReorder={reorder} gap={8}>
              {items.map((a) => (
                <SortableItem key={a.id} id={String(a.id)}>
                  {row(a)}
                </SortableItem>
              ))}
            </SortableWrapper>
          )}
        </Space>
      </LoadGate>

      <Drawer
        open={open}
        onClose={() => setOpen(false)}
        width={520}
        destroyOnHidden
        title={editing ? t('announcementAdmin.editTitle') : t('announcementAdmin.add')}
        footer={
          <Space style={{ width: '100%', justifyContent: 'flex-end' }}>
            <Button onClick={() => setOpen(false)}>{t('common.cancel')}</Button>
            <Button type="primary" loading={saving} onClick={submit}>
              {t('common.save')}
            </Button>
          </Space>
        }
      >
        <Form form={form} layout="vertical">
          <Space size={20} wrap style={{ marginBottom: 12 }}>
            <Form.Item name="enabled" label={t('announcementAdmin.enabled')} valuePropName="checked" noStyle={false} style={{ marginBottom: 0 }}>
              <Switch />
            </Form.Item>
            <Form.Item name="popup" label={t('announcementAdmin.popup')} valuePropName="checked" style={{ marginBottom: 0 }}>
              <Switch />
            </Form.Item>
            <Form.Item name="dismissible" label={t('announcementAdmin.dismissible')} valuePropName="checked" style={{ marginBottom: 0 }}>
              <Switch />
            </Form.Item>
          </Space>
          <Form.Item name="level" label={t('settings.announcementLevel')}>
            <Select
              options={ANNOUNCEMENT_LEVELS.map((level) => ({
                value: level,
                label: (
                  <Space size={8}>
                    <span
                      aria-hidden
                      style={{
                        display: 'inline-block',
                        width: 8,
                        height: 8,
                        borderRadius: 4,
                        background: (token as unknown as Record<string, string>)[LEVEL_DOT[level]],
                      }}
                    />
                    {t(`settings.announcementLevel.${level}`)}
                  </Space>
                ),
              }))}
            />
          </Form.Item>
          <Form.Item name="scope" label={t('announcementAdmin.scope')} extra={t('announcementAdmin.scopeHint')}>
            <Segmented
              options={[
                { value: 'home', label: t('announcementAdmin.scope.home') },
                { value: 'app', label: t('announcementAdmin.scope.app') },
              ]}
            />
          </Form.Item>
          <Form.Item name="audience" label={t('announcementAdmin.audience')}>
            <Radio.Group
              options={[
                { value: 'all', label: t('announcementAdmin.audience.all') },
                { value: 'grant', label: t('announcementAdmin.audience.grant') },
              ]}
              optionType="button"
            />
          </Form.Item>
          <Form.Item shouldUpdate={(a, b) => a.audience !== b.audience} noStyle>
            {({ getFieldValue }) =>
              getFieldValue('audience') === 'grant' ? (
                <Form.Item
                  name="grants"
                  label={t('announcementAdmin.grants')}
                  extra={usersTruncated ? t('announcementAdmin.grantsTruncated', { count: users.length }) : undefined}
                  // Saving a targeted announcement with nobody in its audience stores a row that
                  // reaches no one and reports no problem — the standard path to an operator
                  // concluding the feature is broken. The server refuses it too.
                  rules={[{ required: true, message: t('announcementAdmin.grantsRequired') }]}
                >
                  <Select
                    mode="multiple"
                    allowClear
                    options={principalOptions}
                    optionFilterProp="label"
                    placeholder={t('announcementAdmin.grantsPlaceholder')}
                  />
                </Form.Item>
              ) : null
            }
          </Form.Item>
          <Form.Item
            name="title"
            label={t('settings.announcementTitle')}
            rules={[{ max: 160, message: t('settings.announcementTitleTooLong') }]}
          >
            <Input maxLength={160} showCount placeholder={t('settings.announcementTitlePlaceholder')} />
          </Form.Item>
          <Form.Item
            name="content"
            label={t('settings.announcementContent')}
            rules={[{ max: 2000, message: t('settings.announcementContentTooLong') }]}
          >
            <Input.TextArea
              maxLength={2000}
              showCount
              autoSize={{ minRows: 5, maxRows: 14 }}
              placeholder={t('settings.announcementContentPlaceholder')}
            />
          </Form.Item>
          <Form.Item name="window" label={t('announcementAdmin.window')} extra={t('announcementAdmin.windowHint')}>
            <RangePicker showTime style={{ width: '100%' }} allowEmpty={[true, true]} />
          </Form.Item>
          {livePreview}
        </Form>
      </Drawer>
    </Space>
  )
}
