import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { App, Button, Empty, Input, InputNumber, Modal, Popconfirm, Select, Space, Switch, Tag, TimePicker, Typography } from 'antd'
import { DeleteOutlined, EditOutlined, MinusCircleOutlined, PlusOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import type { RunFreq, RunOverrun, RunPreset, RunPresetAnchor, RunPresetInterval, RunPresetsResp } from '../../api/types'
import { WEEK_ORDER, parseWeeklyRows, presetSummary, weeklyIntervals, wrapsMidnight, type WeeklyRow } from '../../lib/runSchedule'
import { DragHandle, SortableItem, SortableWrapper } from './dnd'
import LoadGate from '../../components/LoadGate'

// Admin editor for preset low-peak scheduling windows (docs/adr/0014). An ordered, drag-sortable
// list (like LinksPage / TypesPage); each preset is edited in a modal whose anchor fields adapt to
// the chosen frequency. A job snapshots the rule at submit, so editing/deleting never disturbs an
// in-flight run.
//
// A *weekly* preset is edited in rows — a set of weekdays sharing one time range — rather than as
// one start/stop weekday pair per occurrence: "Mon, Wed and Fri 09:00–12:00" is one thing an admin
// configured, not three windows to type out. The rows expand to the stored one-interval-per-weekday
// form on save (`weeklyIntervals`) and group back on load (`parseWeeklyRows`), so nothing about the
// wire format, the table, or the resolver changes. A stored window that spans whole days has no row
// form; that preset keeps the per-edge anchor fields.
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
const defaultWeeklyRow = (): WeeklyRow => ({ days: [1], start: '09:00', stop: '12:00' })

// withFreqFields backfills the fields a frequency needs when the frequency changes, keeping the
// user's chosen times instead of resetting the whole window.
const withFreqFields = (a: RunPresetAnchor, freq: RunFreq): RunPresetAnchor => {
  const r = { ...a }
  if (freq === 'weekly' && r.weekday == null) r.weekday = 1
  if ((freq === 'monthly' || freq === 'yearly') && r.day == null) r.day = 1
  if (freq === 'yearly' && r.month == null) r.month = 1
  return r
}

// collapseByTime drops windows that differ only in the weekday, for a frequency that has none:
// the three expanded copies of "Mon/Wed/Fri 09:00–12:00" are one window once the preset is daily.
const collapseByTime = (intervals: RunPresetInterval[]) => {
  const seen = new Set<string>()
  return intervals.filter((iv) => {
    const key = `${iv.start.time}-${iv.stop.time}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

// toWeeklyRows carries the current windows over when the frequency becomes weekly: the times are
// kept, and each window lands on the weekday its start already named (Monday for the frequencies
// that have no weekday). Windows that don't line up as rows keep their times, one row each.
const toWeeklyRows = (intervals: RunPresetInterval[]): WeeklyRow[] => {
  if (intervals.length === 0) return [defaultWeeklyRow()]
  const normalized = intervals.map((iv) => ({ start: withFreqFields(iv.start, 'weekly'), stop: withFreqFields(iv.stop, 'weekly') }))
  return (
    parseWeeklyRows(normalized) ??
    normalized.map((iv) => ({ days: [iv.start.weekday ?? 1], start: iv.start.time || '09:00', stop: iv.stop.time || '12:00' }))
  )
}

// weeklyRowsOf is what the editor opens a stored preset on: rows for a weekly preset that has a row
// form, and null — "edit this one with the per-edge anchor fields" — for anything else.
const weeklyRowsOf = (p: RunPreset): WeeklyRow[] | null => {
  if (p.freq !== 'weekly') return null
  const rows = parseWeeklyRows(p.intervals || [])
  if (!rows) return null
  return rows.length ? rows : [defaultWeeklyRow()]
}

const blankPreset = (): RunPreset => ({
  id: 0,
  label: '',
  freq: 'daily',
  intervals: [defaultInterval('daily')],
  on_overrun: 'next',
  enabled: true,
  invert: false,
  ord: 0,
})

// presetBody is the create/update payload (server ignores id/ord on the body).
const presetBody = (p: RunPreset) => ({
  label: p.label.trim(),
  freq: p.freq,
  intervals: p.intervals,
  on_overrun: p.on_overrun,
  enabled: p.enabled,
  invert: p.invert,
})

export default function RunPresetsEditor() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [presets, setPresets] = useState<RunPreset[]>([])
  const [draft, setDraft] = useState<RunPreset | null>(null)
  // The weekly rows behind the draft, when it has a row form. They live here rather than being
  // re-derived from the draft's intervals on every render: a row whose weekdays the admin has just
  // cleared expands to nothing, and re-deriving would make the half-filled row vanish under the
  // cursor. null = this preset is edited with the per-edge anchor fields.
  const [rows, setRows] = useState<WeeklyRow[] | null>(null)
  const [saving, setSaving] = useState(false)
  // "No preset windows" is what an admin comes here to check, so it waits for the answer: the
  // page's own LoadGate covers /api/admin/batch/config only, not this section's list.
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')

  const load = () => {
    setLoadErr('')
    return api
      .get<RunPresetsResp>('/api/admin/batch/presets')
      .then((r) => {
        setPresets(r.presets || [])
        setLoaded(true)
      })
      .catch((e) => setLoadErr(errText(e, t)))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const ids = useMemo(() => presets.map((p) => String(p.id)), [presets])

  const edit = (p: RunPreset) => {
    setRows(weeklyRowsOf(p))
    setDraft({ ...p })
  }
  const add = () => {
    setRows(null)
    setDraft(blankPreset())
  }

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
    // A row with no weekday picked expands to nothing. Saying so beats saving a preset that
    // quietly lost a window the admin thought they had configured.
    if (rows?.some((r) => r.days.length === 0)) {
      message.error(t('preset.needWeekday'))
      return
    }
    if (draft.intervals.length === 0) {
      message.error(t('preset.needWindow'))
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
      message.error(errText(e, t))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Space direction="vertical" size={10} style={{ width: '100%' }}>
      <LoadGate loading={!loaded && !loadErr} error={loaded ? undefined : loadErr} onRetry={load} minHeight={140} title={t('common.loadFailedContent')}>
      {presets.length === 0 ? (
        <Empty description={t('preset.none')} image={Empty.PRESENTED_IMAGE_SIMPLE} />
      ) : (
        <SortableWrapper ids={ids} onReorder={onReorder} gap={6}>
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
                <DragHandle label={t('common.reorder')} />
                <Switch size="small" checked={p.enabled} onChange={(v) => toggleEnabled(p, v)} />
                <span style={{ fontWeight: 500, minWidth: 80 }}>{p.label || t('preset.untitled')}</span>
                <Typography.Text type="secondary" style={{ flex: 1, minWidth: 120 }}>
                  {presetSummary(p, t)}
                </Typography.Text>
                {p.invert ? <Tag color="orange">{t('preset.invertTag')}</Tag> : <Tag>{t('preset.overrun.' + p.on_overrun)}</Tag>}
                <Button size="small" icon={<EditOutlined />} onClick={() => edit(p)} />
                <Popconfirm title={t('preset.deleteConfirm')} onConfirm={() => remove(p.id)} okText={t('common.ok')} cancelText={t('common.cancel')}>
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </div>
            </SortableItem>
          ))}
        </SortableWrapper>
      )}
      {/* Standing clear of the list: the add button is an action on the whole list, not another
          row of it, and butted up against the last window it read as one. */}
      <div style={{ marginTop: 6 }}>
        <Button type="dashed" icon={<PlusOutlined />} onClick={add}>
          {t('preset.add')}
        </Button>
      </div>
      </LoadGate>

      <Modal
        open={!!draft}
        title={draft?.id ? t('preset.edit') : t('preset.add')}
        onOk={save}
        confirmLoading={saving}
        onCancel={() => setDraft(null)}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        width={560}
        destroyOnHidden
      >
        {draft && <PresetForm draft={draft} onChange={setDraft} rows={rows} onRows={setRows} />}
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

function PresetForm({
  draft,
  onChange,
  rows,
  onRows,
}: {
  draft: RunPreset
  onChange: (p: RunPreset) => void
  rows: WeeklyRow[] | null
  onRows: (r: WeeklyRow[] | null) => void
}) {
  const { t } = useTranslation()
  const set = (patch: Partial<RunPreset>) => onChange({ ...draft, ...patch })
  // Switching to weekly carries the times into rows; switching away collapses the per-weekday
  // copies back to the one window they spell, so the next frequency doesn't show three of it.
  //
  // Going *through* weekly is lossy for the fields weekly has no room for: a monthly window on day
  // 15 comes back on day 1, and two monthly windows sharing a time range come back as one, because
  // rows are keyed by time range alone. That is a change from the old editor, which never stripped
  // a field and so round-tripped by accident. It is left as-is deliberately: the form shows the
  // result — the day inputs read 1, the window list is shorter — before anything is saved, so this
  // is a visible consequence of changing the rule's kind, not a silent rewrite.
  const changeFreq = (freq: RunFreq) => {
    if (freq === 'weekly') {
      const next = toWeeklyRows(draft.intervals)
      onRows(next)
      set({ freq, intervals: weeklyIntervals(next) })
      return
    }
    const kept = draft.freq === 'weekly' ? collapseByTime(draft.intervals) : draft.intervals
    onRows(null)
    set({ freq, intervals: kept.map((ivl) => ({ start: withFreqFields(ivl.start, freq), stop: withFreqFields(ivl.stop, freq) })) })
  }
  const setIv = (i: number, next: RunPresetInterval) => set({ intervals: draft.intervals.map((x, j) => (j === i ? next : x)) })
  const addIv = () => set({ intervals: [...draft.intervals, defaultInterval(draft.freq)] })
  const rmIv = (i: number) => set({ intervals: draft.intervals.filter((_, j) => j !== i) })
  // A row edit rewrites the stored intervals in the same breath, so the draft is always the thing
  // that would be saved and the modal needs no separate commit step.
  const putRows = (next: WeeklyRow[]) => {
    onRows(next)
    set({ intervals: weeklyIntervals(next) })
  }
  const setRow = (i: number, next: WeeklyRow) => putRows((rows ?? []).map((r, j) => (j === i ? next : r)))

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
          {rows
            ? rows.map((r, i) => (
                <Space key={i} wrap align="center">
                  <Select
                    mode="multiple"
                    allowClear
                    value={r.days}
                    onChange={(days: number[]) => setRow(i, { ...r, days })}
                    placeholder={t('preset.pickWeekdays')}
                    style={{ minWidth: 200, maxWidth: 320 }}
                    options={WEEK_ORDER.map((n) => ({ value: n, label: t('run.weekday.' + n) }))}
                  />
                  {/* The two times and the dash between them travel together: when the row is too
                      wide for the modal it is the whole range that drops to the next line, not the
                      stop time on its own with an orphaned dash above it. */}
                  <Space align="center" wrap={false}>
                    <HourPicker value={r.start} onChange={(v) => setRow(i, { ...r, start: v })} />
                    <span>–</span>
                    {wrapsMidnight(r.start, r.stop) && <Tag style={{ marginInlineEnd: 0 }}>{t('preset.nextDay')}</Tag>}
                    <HourPicker value={r.stop} onChange={(v) => setRow(i, { ...r, stop: v })} />
                  </Space>
                  {rows.length > 1 && (
                    <Button size="small" type="text" danger icon={<MinusCircleOutlined />} onClick={() => putRows(rows.filter((_, j) => j !== i))} />
                  )}
                </Space>
              ))
            : draft.intervals.map((ivl, i) => (
                <Space key={i} wrap align="center">
                  <AnchorFields freq={draft.freq} anchor={ivl.start} onChange={(a) => setIv(i, { ...ivl, start: a })} />
                  <span>–</span>
                  <AnchorFields freq={draft.freq} anchor={ivl.stop} onChange={(a) => setIv(i, { ...ivl, stop: a })} />
                  {draft.intervals.length > 1 && (
                    <Button size="small" type="text" danger icon={<MinusCircleOutlined />} onClick={() => rmIv(i)} />
                  )}
                </Space>
              ))}
          <Button
            size="small"
            type="dashed"
            icon={<PlusOutlined />}
            onClick={() => (rows ? putRows([...rows, defaultWeeklyRow()]) : addIv())}
          >
            {t('preset.addInterval')}
          </Button>
        </Space>
      </Field>
      <Field label={t('preset.invert')}>
        <Switch checked={draft.invert} onChange={(v) => set({ invert: v })} />
      </Field>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t('preset.invertHint')}
      </Typography.Text>
      {/* on_overrun is a "missed the window" policy — only meaningful for a normal (run-inside)
          preset; an inverted preset just waits out its blocked hours, so hide it when inverted. */}
      {!draft.invert && (
        <>
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
        </>
      )}
      <Field label={t('preset.enabled')}>
        <Switch checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
      </Field>
    </Space>
  )
}

// HourPicker is the "HH:mm" TimePicker every window edge uses, talking in the stored string rather
// than in dayjs objects so the row model stays plain data.
function HourPicker({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <TimePicker
      format="HH:mm"
      allowClear={false}
      value={dayjs('2000-01-01 ' + (value || '00:00'))}
      onChange={(d) => onChange(d ? d.format('HH:mm') : '00:00')}
    />
  )
}

// AnchorFields renders only the fields the chosen frequency uses (weekly → weekday; monthly → day;
// yearly → month + day; all → a HH:mm time), keeping the stored anchor minimal. Weekly presets are
// normally edited as rows; this path is what a window spanning whole days keeps.
function AnchorFields({ freq, anchor, onChange }: { freq: RunFreq; anchor: RunPresetAnchor; onChange: (a: RunPresetAnchor) => void }) {
  const { t } = useTranslation()
  return (
    <Space wrap>
      {freq === 'weekly' && (
        <Select
          style={{ width: 110 }}
          // Sunday is 0 and the wire drops it (`json:"weekday,omitempty"`), so a missing weekday
          // read back from the server means Sunday — never Monday. The editor's own new anchors
          // always carry an explicit weekday (defaultAnchor / withFreqFields), so nothing relies
          // on the old fallback.
          value={anchor.weekday ?? 0}
          onChange={(w) => onChange({ ...anchor, weekday: w })}
          options={WEEK_ORDER.map((n) => ({ value: n, label: t('run.weekday.' + n) }))}
        />
      )}
      {freq === 'yearly' && (
        <InputNumber min={1} max={12} value={anchor.month ?? 1} onChange={(m) => onChange({ ...anchor, month: m ?? 1 })} addonBefore={t('run.month')} />
      )}
      {(freq === 'monthly' || freq === 'yearly') && (
        <InputNumber min={1} max={31} value={anchor.day ?? 1} onChange={(d) => onChange({ ...anchor, day: d ?? 1 })} addonBefore={t('run.day')} />
      )}
      <HourPicker value={anchor.time || '00:00'} onChange={(v) => onChange({ ...anchor, time: v })} />
    </Space>
  )
}
