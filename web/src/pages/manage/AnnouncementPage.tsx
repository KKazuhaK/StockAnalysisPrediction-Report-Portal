import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Button,
  DatePicker,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
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
import type { AdminAnnouncement, AnnouncementLevel } from '../../api/types'
import { AnnouncementAlert } from '../../components/SiteAnnouncement'
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

interface FormValues {
  enabled: boolean
  popup: boolean
  dismissible: boolean
  level: AnnouncementLevel
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
      .get<{ items: AdminAnnouncement[] }>('/api/admin/announcements')
      .then((r) => {
        setItems(r.items || [])
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

  const remove = async (id: number) => {
    await api.del(`/api/admin/announcements/${id}`)
    await refreshed()
  }

  // Only the first popup-eligible announcement actually interrupts a reader, so the switches below
  // it are decorative this page load. Saying so on the row is the point: an ignored setting should
  // be visible where it is set, not discovered by an operator wondering why nothing popped up.
  const firstPopupId = items.find((a) => a.popup && a.enabled)?.id

  const statusOf = (a: AdminAnnouncement) => {
    if (!a.enabled) return { key: 'draft', color: undefined }
    const now = Date.now()
    if (a.startsAt && Date.parse(a.startsAt) > now) return { key: 'scheduled', color: 'blue' }
    if (a.endsAt && Date.parse(a.endsAt) <= now) return { key: 'expired', color: undefined }
    return { key: 'live', color: 'green' }
  }

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
        <Tag color={status.color}>{t(`announcementAdmin.status.${status.key}`)}</Tag>
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
          <Switch size="small" checked={a.enabled} onChange={(v) => toggle(a, 'enabled', v)} />
        </Tooltip>
        <Button size="small" icon={<EditOutlined />} onClick={() => openEdit(a)} />
        <Popconfirm title={t('announcementAdmin.deleteConfirm')} onConfirm={() => remove(a.id)}>
          <Button size="small" danger icon={<DeleteOutlined />} />
        </Popconfirm>
      </div>
    )
  }

  const preview = useMemo(
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
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('announcementAdmin.hint')}
            </Typography.Text>
          </Space>

          {items.length === 0 ? (
            <Empty description={t('announcementAdmin.empty')} />
          ) : (
            // Dragging is disabled until the list has actually loaded: reorder replaces the whole
            // order in one call, and a page that never rendered has no order worth sending.
            <SortableWrapper ids={loaded ? ids : []} onReorder={reorder}>
              <Space direction="vertical" size={8} style={{ width: '100%' }}>
                {items.map((a) => (
                  <SortableItem key={a.id} id={String(a.id)}>
                    {row(a)}
                  </SortableItem>
                ))}
              </Space>
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
          {preview}
        </Form>
      </Drawer>
    </Space>
  )
}
