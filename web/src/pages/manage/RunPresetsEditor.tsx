import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { App, Button, Empty, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Tag, TimePicker, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, MinusCircleOutlined, PlusOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import type { RunFreq, RunOverrun, RunPreset, RunPresetAnchor, RunPresetInterval, RunPresetsResp } from '../../api/types'
import { presetSummary } from '../../lib/runSchedule'
import { DragHandle, SortableItem, SortableWrapper } from './dnd'

// Admin editor for preset low-peak scheduling windows (docs/adr/0014). An ordered, drag-sortable
// list (like LinksPage / TypesPage); each preset is edited in a modal whose anchor fields adapt to
// the chosen frequency. A job snapshots the rule at submit, so editing/deleting never disturbs an
// in-flight run.
const FREQS: RunFreq[] = ['daily', 'weekly', 'monthly', 'yearly']
const OVERRUNS: RunOverrun[] = ['next', 'continue', 'cancel']

// defaultAnchor seeds a new anchor with only the fields the frequency uses.
const defaultAnchor = (freq: RunFreq, time: string): RunPresetAnchor => {
  const a: RunPresetAnchor = { time }
  if (freq === 'weekly') a.weekday = 1
  if (freq === 'monthly' || freq === 'yearly') a.day = 1
  if (freq === 'yearly') a.month = 1
  return a
}
const defaultInterval = (freq: RunFreq): RunPresetInterval => ({
  start: defaultAnchor(freq, '09:00'),
  stop: defaultAnchor(freq, '12:00'),
})
// withFreqFields backfills the fields a frequency needs when the frequency changes, keeping the
// user's chosen times instead of resetting the whole window.
const withFreqFields = (a: RunPresetAnchor, freq: RunFreq): RunPresetAnchor => {
  const r = { ...a }
  if (freq === 'weekly' && r.weekday == null) r.weekday = 1
  if ((freq === 'monthly' || freq === 'yearly') && r.day == null) r.day = 1
  if (freq === 'yearly' && r.month == null) r.month = 1
  return r
}

const blankPreset = (): RunPreset => ({
  id: 0,
  label: '',
  freq: 'daily',
  intervals: [defaultInterval('daily')],
  on_overrun: 'next',
  enabled: true,
  ord: 0,
})

// presetBody is the create/update payload (server ignores id/ord on the body).
const presetBody = (p: RunPreset) => ({
  label: p.label.trim(),
  freq: p.freq,
  intervals: p.intervals,
  on_overrun: p.on_overrun,
  enabled: p.enabled,
})

export default function RunPresetsEditor() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [presets, setPresets] = useState<RunPreset[]>([])
  const [draft, setDraft] = useState<RunPreset | null>(null)
  const [saving, setSaving] = useState(false)

  const load = () =>
    api
      .get<RunPresetsResp>('/api/admin/batch/presets')
      .then((r) => setPresets(r.presets || []))
      .catch(() => {})
  useEffect(() => {
    load()
  }, [])

  const ids = useMemo(() => presets.map((p) => String(p.id)), [presets])

  const toggleEnabled = async (p: RunPreset, enabled: boolean) => {
    await api.put(`/api/admin/batch/presets/${p.id}`, { ...presetBody(p), enabled })
    load()
  }
  const remove = async (id: number) => {
    await api.del(`/api/admin/batch/presets/${id}`)
    load()
  }
  const onReorder = (orderedIds: string[]) => {
    setPresets((cur) => orderedIds.map((id) => cur.find((p) => String(p.id) === id)).filter((p): p is RunPreset => Boolean(p)))
    api.post('/api/admin/batch/presets/reorder', { ids: orderedIds.map(Number) }).catch(() => load())
  }

  const save = async () => {
    if (!draft) return
    if (!draft.label.trim()) {
      message.error(t('preset.needLabel'))
      return
    }
    setSaving(true)
    try {
      if (draft.id) await api.put(`/api/admin/batch/presets/${draft.id}`, presetBody(draft))
      else await api.post('/api/admin/batch/presets', presetBody(draft))
      message.success(t('common.saved'))
      setDraft(null)
      load()
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Space direction="vertical" size={10} style={{ width: '100%' }}>
      {presets.length === 0 ? (
        <Empty description={t('preset.none')} image={Empty.PRESENTED_IMAGE_SIMPLE} />
      ) : (
        <SortableWrapper ids={ids} onReorder={onReorder}>
          <Space direction="vertical" size={6} style={{ width: '100%' }}>
            {presets.map((p) => (
              <SortableItem key={p.id} id={String(p.id)}>
                <div
                  style={{
                    display: 'flex',
                    alignItems: 'center',
                    gap: 8,
                    padding: '6px 8px',
                    border: '1px solid rgba(128,128,128,0.2)',
                    borderRadius: 8,
                  }}
                >
                  <DragHandle />
                  <Switch size="small" checked={p.enabled} onChange={(v) => toggleEnabled(p, v)} />
                  <span style={{ fontWeight: 500, minWidth: 80 }}>{p.label || t('preset.untitled')}</span>
                  <Typography.Text type="secondary" style={{ flex: 1, minWidth: 120 }}>
                    {presetSummary(p, t)}
                  </Typography.Text>
                  <Tag>{t('preset.overrun.' + p.on_overrun)}</Tag>
                  <Button size="small" icon={<EditOutlined />} onClick={() => setDraft({ ...p })} />
                  <Popconfirm title={t('preset.deleteConfirm')} onConfirm={() => remove(p.id)} okText={t('common.ok')} cancelText={t('common.cancel')}>
                    <Button size="small" danger icon={<DeleteOutlined />} />
                  </Popconfirm>
                </div>
              </SortableItem>
            ))}
          </Space>
        </SortableWrapper>
      )}
      <Button icon={<PlusOutlined />} onClick={() => setDraft(blankPreset())}>
        {t('preset.add')}
      </Button>

      <Modal
        open={!!draft}
        title={draft?.id ? t('preset.edit') : t('preset.add')}
        onOk={save}
        confirmLoading={saving}
        onCancel={() => setDraft(null)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        destroyOnClose
      >
        {draft && <PresetForm draft={draft} onChange={setDraft} />}
      </Modal>
    </Space>
  )
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Space wrap align="center">
      <span style={{ display: 'inline-block', minWidth: 72 }}>{label}</span>
      {children}
    </Space>
  )
}

function PresetForm({ draft, onChange }: { draft: RunPreset; onChange: (p: RunPreset) => void }) {
  const { t } = useTranslation()
  const set = (patch: Partial<RunPreset>) => onChange({ ...draft, ...patch })
  const changeFreq = (freq: RunFreq) =>
    set({ freq, intervals: draft.intervals.map((ivl) => ({ start: withFreqFields(ivl.start, freq), stop: withFreqFields(ivl.stop, freq) })) })
  const setIv = (i: number, next: RunPresetInterval) => set({ intervals: draft.intervals.map((x, j) => (j === i ? next : x)) })
  const addIv = () => set({ intervals: [...draft.intervals, defaultInterval(draft.freq)] })
  const rmIv = (i: number) => set({ intervals: draft.intervals.filter((_, j) => j !== i) })
  return (
    <Space direction="vertical" size={12} style={{ width: '100%' }}>
      <Field label={t('preset.label')}>
        <Input value={draft.label} onChange={(e) => set({ label: e.target.value })} placeholder={t('preset.labelPlaceholder')} style={{ maxWidth: 260 }} />
      </Field>
      <Field label={t('preset.freq')}>
        <Select
          value={draft.freq}
          onChange={(f) => changeFreq(f as RunFreq)}
          style={{ width: 160 }}
          options={FREQS.map((f) => ({ value: f, label: t('run.freq.' + f) }))}
        />
      </Field>
      <Field label={t('preset.windows')}>
        <Space direction="vertical" size={6} style={{ width: '100%' }}>
          {draft.intervals.map((ivl, i) => (
            <Space key={i} wrap align="center">
              <AnchorFields freq={draft.freq} anchor={ivl.start} onChange={(a) => setIv(i, { ...ivl, start: a })} />
              <span>–</span>
              <AnchorFields freq={draft.freq} anchor={ivl.stop} onChange={(a) => setIv(i, { ...ivl, stop: a })} />
              {draft.intervals.length > 1 && (
                <Button size="small" type="text" danger icon={<MinusCircleOutlined />} onClick={() => rmIv(i)} />
              )}
            </Space>
          ))}
          <Button size="small" type="dashed" icon={<PlusOutlined />} onClick={addIv}>
            {t('preset.addInterval')}
          </Button>
        </Space>
      </Field>
      <Field label={t('preset.overrunLabel')}>
        <Select
          value={draft.on_overrun}
          onChange={(o) => set({ on_overrun: o as RunOverrun })}
          style={{ width: 220 }}
          options={OVERRUNS.map((o) => ({ value: o, label: t('preset.overrun.' + o) }))}
        />
      </Field>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t('preset.overrunHint')}
      </Typography.Text>
      <Field label={t('preset.enabled')}>
        <Switch checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
      </Field>
    </Space>
  )
}

// AnchorFields renders only the fields the chosen frequency uses (weekly → weekday; monthly → day;
// yearly → month + day; all → a HH:mm time), keeping the stored anchor minimal.
function AnchorFields({ freq, anchor, onChange }: { freq: RunFreq; anchor: RunPresetAnchor; onChange: (a: RunPresetAnchor) => void }) {
  const { t } = useTranslation()
  const time = anchor.time || '00:00'
  return (
    <Space wrap>
      {freq === 'weekly' && (
        <Select
          style={{ width: 110 }}
          value={anchor.weekday ?? 1}
          onChange={(w) => onChange({ ...anchor, weekday: w })}
          options={[0, 1, 2, 3, 4, 5, 6].map((n) => ({ value: n, label: t('run.weekday.' + n) }))}
        />
      )}
      {freq === 'yearly' && (
        <InputNumber min={1} max={12} value={anchor.month ?? 1} onChange={(m) => onChange({ ...anchor, month: m ?? 1 })} addonBefore={t('run.month')} />
      )}
      {(freq === 'monthly' || freq === 'yearly') && (
        <InputNumber min={1} max={31} value={anchor.day ?? 1} onChange={(d) => onChange({ ...anchor, day: d ?? 1 })} addonBefore={t('run.day')} />
      )}
      <TimePicker
        format="HH:mm"
        allowClear={false}
        value={dayjs('2000-01-01 ' + time)}
        onChange={(d) => onChange({ ...anchor, time: d ? d.format('HH:mm') : '00:00' })}
      />
    </Space>
  )
}
