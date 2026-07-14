import { useEffect, useMemo, useState } from 'react'
import { App, Button, Card, Drawer, Input, InputNumber, Modal, Select, Space, Switch, Table, Tag, TimePicker, Typography } from 'antd'
import { ClockCircleOutlined, DeleteOutlined, EditOutlined, HistoryOutlined, PlayCircleOutlined, PlusOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useAuth } from '../auth'
import type { BatchTarget, RecurringDetail, RecurringRun, RecurringTask, RecurringTasksResp } from '../api/types'
import { csvToRows, toCSV } from '../lib/csv'

// Recurring-tasks console (计划任务; docs/adr/0018-recurring-tasks.md). A first-party app card
// (gated by run_batch) that manages saved job templates + a daily/weekly/monthly cadence the server
// fires into the run queue. It talks to /api/admin/batch/recurring/* (same channel as the batch
// console, ADR 0018 §7) — the actual firing is a trusted backend loop, not this UI.

// Blank draft for the create form.
const emptyDraft = {
  name: '',
  targetId: undefined as number | undefined,
  freq: 'daily' as RecurringTask['freq'],
  atTime: '09:30',
  weekday: 1,
  monthday: 1,
  priority: '' as string, // '' normal | 'idle'
  concurrency: 1,
  maxRetries: 2,
  enabled: true,
  csvText: '',
}
type Draft = typeof emptyDraft

export default function RecurringConsole() {
  const { t } = useTranslation()
  const { message, modal } = App.useApp()
  const { admin } = useAuth()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [tasks, setTasks] = useState<RecurringTask[]>([])
  const [loading, setLoading] = useState(false)

  const [modalOpen, setModalOpen] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null) // null = create
  const [draft, setDraft] = useState<Draft>(emptyDraft)
  // Columns for the CSV editor when the task's target has been deleted (no discovered inputs to fall
  // back on) — derived from the stored template so editing an orphaned task never blanks its rows.
  const [fallbackKeys, setFallbackKeys] = useState<string[]>([])
  const [saving, setSaving] = useState(false)

  const [history, setHistory] = useState<{ task: RecurringTask; runs: RecurringRun[] } | null>(null)

  const loadTargets = () => api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || []))
  const loadTasks = () => {
    setLoading(true)
    return api
      .get<RecurringTasksResp>('/api/admin/batch/recurring')
      .then((r) => setTasks(r.tasks || []))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    loadTargets()
    loadTasks()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const draftTarget = useMemo(() => targets.find((tg) => tg.id === draft.targetId), [targets, draft.targetId])
  const targetKeys = useMemo(() => (draftTarget?.inputs || []).map((i) => i.key), [draftTarget])
  // Prefer the live target's discovered inputs; fall back to the stored template's own columns so a
  // task whose target was deleted still shows and preserves its rows in the editor.
  const inputKeys = useMemo(() => (targetKeys.length ? targetKeys : fallbackKeys), [targetKeys, fallbackKeys])
  const rows = useMemo(() => (inputKeys.length ? csvToRows(draft.csvText, inputKeys) : []), [draft.csvText, inputKeys])
  const targetDeleted = draft.targetId != null && !draftTarget

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setDraft((d) => ({ ...d, [k]: v }))

  // The priority Select shows a "mode"; the stored draft.priority is '' | 'idle' | 'urgent' | a number.
  const priorityMode = draft.priority === '' ? 'normal' : draft.priority === 'idle' ? 'idle' : draft.priority === 'urgent' ? 'urgent' : 'custom'
  const setPriorityMode = (m: string) => set('priority', m === 'normal' ? '' : m === 'custom' ? '80' : m)

  // pickTarget selects a target and pre-fills (or swaps) the CSV header so the columns are visible and
  // the user only types data rows. It preserves any data the user already entered — it only replaces
  // an empty editor or one that still holds the previous target's bare header line.
  const pickTarget = (v: number | undefined) => {
    const newKeys = (targets.find((tg) => tg.id === v)?.inputs || []).map((i) => i.key)
    const oldHeader = inputKeys.join(',')
    setDraft((d) => {
      const keepBody = d.csvText.trim() !== '' && d.csvText.trim() !== oldHeader
      return { ...d, targetId: v, csvText: keepBody ? d.csvText : newKeys.join(',') }
    })
  }

  const openCreate = () => {
    setEditingId(null)
    setDraft(emptyDraft)
    setFallbackKeys([])
    setModalOpen(true)
  }

  const openEdit = async (task: RecurringTask) => {
    // Fetch the full template to prefill the CSV editor.
    const d = await api.get<RecurringDetail>(`/api/admin/batch/recurring/${task.id}`)
    // Columns from the live target's inputs, or (target deleted) the stored template's own keys.
    const tgt = targets.find((tg) => tg.id === d.target_id)
    const keys = tgt?.inputs?.length ? tgt.inputs.map((i) => i.key) : Object.keys((d.rows || [])[0] || {})
    // Rebuild a proper CSV WITH a header row (csvToRows is header-based) so the round-trip preserves
    // the template instead of consuming the first data row as a header.
    const csvText = toCSV(keys, (d.rows || []).map((r) => keys.map((k) => r[k] ?? '')))
    setFallbackKeys(keys)
    setEditingId(task.id)
    setDraft({
      name: d.name,
      targetId: d.target_id,
      freq: d.freq as RecurringTask['freq'],
      atTime: d.at_time || '09:30',
      weekday: d.weekday,
      monthday: d.monthday,
      priority: d.priority || '',
      concurrency: d.concurrency || 1,
      maxRetries: d.max_retries || 0,
      enabled: d.enabled,
      csvText,
    })
    setModalOpen(true)
  }

  const save = async () => {
    if (!draft.name.trim()) {
      message.error(t('recurring.errName'))
      return
    }
    if (!draft.targetId || rows.length === 0) {
      message.error(t('recurring.errRows'))
      return
    }
    setSaving(true)
    try {
      const body = {
        name: draft.name.trim(),
        target_id: draft.targetId,
        rows,
        concurrency: draft.concurrency,
        max_retries: draft.maxRetries,
        priority: draft.priority,
        freq: draft.freq,
        at_time: draft.atTime,
        weekday: draft.weekday,
        monthday: draft.monthday,
        enabled: draft.enabled,
      }
      if (editingId == null) await api.post('/api/admin/batch/recurring', body)
      else await api.put(`/api/admin/batch/recurring/${editingId}`, body)
      message.success(t('common.saved'))
      setModalOpen(false)
      loadTasks()
    } catch (e) {
      message.error((e as Error).message || t('recurring.errSave'))
    } finally {
      setSaving(false)
    }
  }

  const toggle = async (task: RecurringTask, enabled: boolean) => {
    try {
      await api.post(`/api/admin/batch/recurring/${task.id}/enable`, { enabled })
      loadTasks()
    } catch (e) {
      message.error((e as Error).message)
    }
  }

  const runNow = (task: RecurringTask) =>
    modal.confirm({
      title: t('recurring.runNowTitle', { name: task.name }),
      content: t('recurring.runNowBody'),
      okText: t('recurring.runNow'),
      cancelText: t('common.cancel'),
      onOk: async () => {
        const r = await api.post<{ job_id: number }>(`/api/admin/batch/recurring/${task.id}/run`, {})
        message.success(t('recurring.firedJob', { id: r.job_id }))
        loadTasks()
      },
    })

  const remove = (task: RecurringTask) =>
    modal.confirm({
      title: t('recurring.deleteTitle', { name: task.name }),
      content: t('recurring.deleteBody'),
      okText: t('common.delete'),
      cancelText: t('common.cancel'),
      okButtonProps: { danger: true },
      onOk: async () => {
        await api.del(`/api/admin/batch/recurring/${task.id}`)
        message.success(t('recurring.deleted'))
        loadTasks()
      },
    })

  const openHistory = async (task: RecurringTask) => {
    const d = await api.get<RecurringDetail>(`/api/admin/batch/recurring/${task.id}`)
    setHistory({ task, runs: d.history || [] })
  }

  const cadenceLabel = (task: RecurringTask) => {
    if (task.freq === 'daily') return t('recurring.cadence.daily', { time: task.at_time })
    if (task.freq === 'weekly') return t('recurring.cadence.weekly', { day: t(`run.weekday.${task.weekday}`), time: task.at_time })
    if (task.freq === 'monthly') return t('recurring.cadence.monthly', { day: task.monthday, time: task.at_time })
    return task.freq
  }

  const columns = [
    {
      title: t('recurring.colName'),
      dataIndex: 'name',
      render: (name: string, task: RecurringTask) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{name}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {task.target_name || t('recurring.targetGone')} · {t('recurring.rowsN', { n: task.row_count })}
          </Typography.Text>
        </Space>
      ),
    },
    { title: t('recurring.colCadence'), key: 'cadence', render: (_: unknown, task: RecurringTask) => cadenceLabel(task) },
    {
      title: t('recurring.colPriority'),
      dataIndex: 'priority',
      render: (p: string) => {
        if (p === 'idle') return <Tag>{t('recurring.priorityIdle')}</Tag>
        if (p === 'urgent') return <Tag color="red">{t('recurring.priorityUrgent')}</Tag>
        if (p !== '' && !Number.isNaN(Number(p))) return <Tag color="gold">{t('recurring.priorityBase', { n: p })}</Tag>
        return <Tag color="blue">{t('recurring.priorityNormal')}</Tag>
      },
    },
    {
      title: t('recurring.colNext'),
      dataIndex: 'next_run',
      render: (v: string, task: RecurringTask) => (task.enabled ? v || '—' : <Typography.Text type="secondary">{t('recurring.paused')}</Typography.Text>),
    },
    { title: t('recurring.colLastFired'), dataIndex: 'last_fired', render: (v: string) => v || <Typography.Text type="secondary">{t('recurring.never')}</Typography.Text> },
    {
      title: t('recurring.colEnabled'),
      dataIndex: 'enabled',
      render: (enabled: boolean, task: RecurringTask) => <Switch checked={enabled} onChange={(v) => toggle(task, v)} />,
    },
    {
      title: t('recurring.colActions'),
      key: 'actions',
      render: (_: unknown, task: RecurringTask) => (
        <Space>
          <Button size="small" icon={<PlayCircleOutlined />} onClick={() => runNow(task)}>
            {t('recurring.runNow')}
          </Button>
          <Button size="small" icon={<HistoryOutlined />} aria-label={t('recurring.history')} onClick={() => openHistory(task)} />
          <Button size="small" icon={<EditOutlined />} aria-label={t('common.edit')} onClick={() => openEdit(task)} />
          <Button size="small" danger icon={<DeleteOutlined />} aria-label={t('common.delete')} onClick={() => remove(task)} />
        </Space>
      ),
    },
  ]

  const freqOptions = [
    { value: 'daily', label: t('run.freq.daily') },
    { value: 'weekly', label: t('run.freq.weekly') },
    { value: 'monthly', label: t('run.freq.monthly') },
  ]
  const weekdayOptions = [0, 1, 2, 3, 4, 5, 6].map((d) => ({ value: d, label: t(`run.weekday.${d}`) }))

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <ClockCircleOutlined />
            {t('recurring.title')}
          </Space>
        }
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate} disabled={targets.length === 0}>
            {t('recurring.new')}
          </Button>
        }
      >
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('recurring.intro')}</Typography.Text>
          {targets.length === 0 && <Typography.Text type="warning">{t('recurring.noTargets')}</Typography.Text>}
          <div style={{ overflowX: 'auto' }}>
            <Table
              rowKey="id"
              size="small"
              loading={loading}
              pagination={false}
              dataSource={tasks}
              columns={columns}
              locale={{ emptyText: t('recurring.empty') }}
            />
          </div>
        </Space>
      </Card>

      <Modal
        open={modalOpen}
        title={editingId == null ? t('recurring.new') : t('recurring.edit')}
        onCancel={() => setModalOpen(false)}
        onOk={save}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        confirmLoading={saving}
        width={640}
        destroyOnHidden
      >
        <Space direction="vertical" size={12} style={{ width: '100%', marginTop: 8 }}>
          <div>
            <Typography.Text>{t('recurring.fieldName')}</Typography.Text>
            <Input value={draft.name} onChange={(e) => set('name', e.target.value)} placeholder={t('recurring.namePlaceholder')} />
          </div>

          <div>
            <Typography.Text>{t('recurring.fieldTarget')}</Typography.Text>
            <Select
              showSearch
              optionFilterProp="label"
              style={{ width: '100%' }}
              placeholder={t('batch.selectTarget')}
              value={draft.targetId}
              onChange={pickTarget}
              options={[
                ...targets.map((tg) => ({ value: tg.id, label: tg.plugin_name ? `${tg.name}（${tg.plugin_name}）` : tg.name })),
                // Keep the deleted target's slot labelled so the picker doesn't show a bare id.
                ...(targetDeleted ? [{ value: draft.targetId as number, label: t('recurring.targetGone') }] : []),
              ]}
            />
            {targetDeleted && <Typography.Text type="warning">{t('recurring.targetDeletedWarn')}</Typography.Text>}
          </div>

          {inputKeys.length > 0 && (
            <div>
              <Typography.Text type="secondary">{t('recurring.rowsHint')}</Typography.Text>{' '}
              {inputKeys.map((k) => (
                <Tag key={k}>{k}</Tag>
              ))}
              <Input.TextArea
                rows={4}
                style={{ marginTop: 6 }}
                value={draft.csvText}
                onChange={(e) => set('csvText', e.target.value)}
                placeholder={t('batch.csvPlaceholder', { keys: inputKeys.join(',') })}
              />
              <Typography.Text type="secondary">{t('batch.parsedRows', { n: rows.length })}</Typography.Text>
            </div>
          )}

          <Space wrap align="center">
            <Typography.Text>{t('recurring.fieldCadence')}</Typography.Text>
            <Select style={{ width: 130 }} value={draft.freq} onChange={(v) => set('freq', v)} options={freqOptions} />
            <TimePicker
              format="HH:mm"
              allowClear={false}
              needConfirm={false}
              value={dayjs('2000-01-01 ' + draft.atTime)}
              onChange={(d) => set('atTime', d ? d.format('HH:mm') : '09:30')}
              aria-label={t('recurring.fieldCadence')}
            />
            {draft.freq === 'weekly' && <Select style={{ width: 120 }} value={draft.weekday} onChange={(v) => set('weekday', v)} options={weekdayOptions} />}
            {draft.freq === 'monthly' && (
              <Space size={4}>
                <Typography.Text type="secondary">{t('recurring.dayOfMonth')}</Typography.Text>
                <InputNumber min={1} max={31} value={draft.monthday} onChange={(v) => set('monthday', v ?? 1)} />
              </Space>
            )}
          </Space>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {t('recurring.tzHint')}
          </Typography.Text>

          <Space wrap>
            <Typography.Text>{t('recurring.fieldPriority')}</Typography.Text>
            {/* Priority mode: normal/idle for anyone; admins additionally get urgent (top priority) and
                a custom base score. The stored value is '' | 'idle' | 'urgent' | a number string. */}
            <Select
              style={{ width: 220 }}
              value={priorityMode}
              onChange={setPriorityMode}
              options={[
                { value: 'normal', label: t('recurring.priorityNormal') },
                { value: 'idle', label: t('recurring.priorityIdleLong') },
                ...(admin
                  ? [
                      { value: 'urgent', label: t('recurring.priorityUrgent') },
                      { value: 'custom', label: t('recurring.priorityCustom') },
                    ]
                  : []),
              ]}
            />
            {admin && priorityMode === 'custom' && (
              <InputNumber min={0} max={100} value={Number(draft.priority) || 0} onChange={(v) => set('priority', String(v ?? 0))} aria-label={t('recurring.priorityCustom')} />
            )}
            <Typography.Text>{t('batch.rowConcurrency')}</Typography.Text>
            <InputNumber min={1} max={20} value={draft.concurrency} onChange={(v) => set('concurrency', v ?? 1)} />
            <Typography.Text>{t('batch.maxRetries')}</Typography.Text>
            <InputNumber min={0} max={5} value={draft.maxRetries} onChange={(v) => set('maxRetries', v ?? 0)} />
          </Space>

          <Space>
            <Switch checked={draft.enabled} onChange={(v) => set('enabled', v)} />
            <Typography.Text>{t('recurring.fieldEnabled')}</Typography.Text>
          </Space>
        </Space>
      </Modal>

      <Drawer
        open={history != null}
        onClose={() => setHistory(null)}
        width={440}
        title={history ? t('recurring.historyTitle', { name: history.task.name }) : ''}
      >
        <Table
          rowKey="id"
          size="small"
          pagination={false}
          dataSource={history?.runs || []}
          locale={{ emptyText: t('recurring.never') }}
          columns={[
            { title: t('recurring.histFired'), dataIndex: 'fired_at' },
            {
              title: t('recurring.histJob'),
              dataIndex: 'job_id',
              render: (id: number, r: RecurringRun) => (
                <Space size={6}>
                  <Typography.Text>#{id}</Typography.Text>
                  {r.job_status && <Tag>{r.job_status}</Tag>}
                </Space>
              ),
            },
          ]}
        />
      </Drawer>
    </Space>
  )
}
