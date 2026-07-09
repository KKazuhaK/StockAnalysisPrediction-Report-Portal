import { useEffect, useMemo, useState } from 'react'
import { Alert, App, Checkbox, Form, Input, InputNumber, Modal, Select, Space, Typography } from 'antd'
import { PlayCircleOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useAuth } from '../auth'
import { difyModeKind } from '../lib/batchUi'
import { emptySchedule, schedulePayload, scheduleError, type RunSchedule } from '../lib/runSchedule'
import RunScheduleControls from './RunScheduleControls'
import type { BatchQueueSummary, BatchTarget, BatchTickets, RunPreset, RunPresetsResp } from '../api/types'

// The home-page run-analysis modal (docs/adr/0007 + 0014): pick a Dify workflow, fill its
// discovered inputs, choose when to run (now / a preset low-peak window / an explicit 定时 time)
// and the priority lane (加急 / 队列空闲), with the live queue depth shown inline. The run-time +
// priority controls are the shared RunScheduleControls, reused by the batch console.
export default function RunAnalysisModal({
  open,
  onClose,
  onSubmitted,
  initialTargetId,
}: {
  open: boolean
  onClose: () => void
  onSubmitted?: (jobId: number) => void
  initialTargetId?: number // pre-select this workflow when opened from a pinned entry-button shortcut
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { email, mailEnabled } = useAuth()
  const [form] = Form.useForm()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [targetId, setTargetId] = useState<number | undefined>()
  const [tickets, setTickets] = useState<BatchTickets | null>(null)
  const [queue, setQueue] = useState<BatchQueueSummary | null>(null)
  const [presets, setPresets] = useState<RunPreset[]>([])
  const [schedule, setSchedule] = useState<RunSchedule>(emptySchedule)
  const [notify, setNotify] = useState(false)
  const [retries, setRetries] = useState(0) // failure retries; 0 = never auto-retry (a single run maps 1:1 to the click)
  const [submitting, setSubmitting] = useState(false)

  useEffect(() => {
    if (!open) return
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || [])).catch(() => {})
    api.get<BatchTickets>('/api/admin/batch/tickets').then(setTickets).catch(() => {})
    api.get<BatchQueueSummary>('/api/admin/batch/queue').then(setQueue).catch(() => {})
    // Presets + the admin-set run-form defaults (default mode button + idle pre-check).
    api
      .get<RunPresetsResp>('/api/admin/batch/presets')
      .then((r) => {
        setPresets(r.presets || [])
        // Fall back to immediate if the admin default is "preset" but no preset windows are enabled
        // (the preset mode button is hidden then, so it couldn't be selected anyway).
        const hasPresets = (r.presets || []).some((p) => p.enabled)
        const mode = r.default_mode === 'preset' && !hasPresets ? 'now' : r.default_mode || 'now'
        setSchedule((s) => ({ ...s, mode, idle: !!r.default_idle }))
      })
      .catch(() => {})
  }, [open])

  // 运行分析 generates a report (Dify ingests it). Agent (agent-chat) apps are conversational and
  // don't post a report to the portal, so they're excluded here — they belong in the 助手 chat page.
  const runnable = useMemo(() => targets.filter((tg) => difyModeKind(tg.mode) !== 'agent'), [targets])
  const enabledPresets = useMemo(() => presets.filter((p) => p.enabled), [presets])
  const target = useMemo(() => targets.find((tg) => tg.id === targetId), [targets, targetId])
  const inputs = target?.inputs || []

  // A pinned entry-button shortcut opens the modal with a specific workflow already chosen.
  useEffect(() => {
    if (!open || initialTargetId == null) return
    if (runnable.some((tg) => tg.id === initialTargetId)) {
      setTargetId(initialTargetId)
      form.resetFields()
    }
  }, [open, initialTargetId, runnable])

  const urgentEnabled = tickets?.urgent_enabled !== false
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0
  useEffect(() => {
    if ((!urgentEnabled || urgentDisabled) && schedule.urgent) setSchedule((s) => ({ ...s, urgent: false }))
  }, [urgentEnabled, urgentDisabled, schedule.urgent])

  const pickTarget = (id: number) => {
    setTargetId(id)
    form.resetFields()
  }

  const reset = () => {
    setTargetId(undefined)
    setNotify(false)
    setRetries(0)
    setSchedule(emptySchedule)
    form.resetFields()
  }

  const submit = async () => {
    if (!targetId) {
      message.error(t('run.selectWorkflow'))
      return
    }
    let vals: Record<string, unknown>
    try {
      vals = await form.validateFields()
    } catch {
      return
    }
    const err = scheduleError(schedule)
    if (err) {
      message.error(t(err))
      return
    }
    setSubmitting(true)
    try {
      const row: Record<string, string> = {}
      inputs.forEach((i) => {
        row[i.key] = String(vals[i.key] ?? '').trim()
      })
      const sp = schedulePayload(schedule)
      const res = await api.post<{ job_id: number; downgraded?: boolean; run_at?: string }>('/api/admin/batch/jobs', {
        target_id: targetId,
        concurrency: 1,
        max_retries: retries, // default 0 (no auto-retry); user can opt into failure retries, same as batch
        priority: sp.priority, // "urgent" | "idle" | "" (backend resolves the "" default)
        run_at: sp.run_at,
        preset_id: sp.preset_id,
        notify,
        rows: [row],
      })
      if (res.run_at) message.success(t('run.scheduledOk', { at: res.run_at }))
      else message.success(t('run.startedOk', { id: res.job_id }))
      if (res.downgraded) message.warning(t('batch.ticketDowngraded'))
      onSubmitted?.(res.job_id)
      reset()
      onClose()
    } catch (e) {
      message.error((e as Error).message || t('run.startFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  // Three distinct states so the banner never misleads: a run starts immediately only when a run
  // slot is free (concurrent runs < the run cap); otherwise it queues. Count actual concurrent
  // runs (rows), the unit the cap governs — not whole jobs.
  const waiting = queue?.waiting ?? 0
  const running = queue?.running_rows ?? queue?.running ?? 0
  const budget = queue?.budget ?? 1
  const busy = running >= budget
  const queueMsg = busy
    ? t('run.queueBusy', { n: running, ahead: waiting })
    : running + waiting === 0
      ? t('run.queueIdle')
      : t('run.queueFree', { n: budget - running })

  return (
    <Modal
      title={
        <Space>
          <PlayCircleOutlined />
          {t('run.title')}
        </Space>
      }
      open={open}
      onOk={submit}
      okText={schedule.mode === 'now' ? t('run.run') : t('run.schedule')}
      okButtonProps={{ loading: submitting, disabled: !targetId }}
      cancelText={t('common.cancel')}
      onCancel={onClose}
      destroyOnClose
    >
      <Space direction="vertical" size={14} style={{ width: '100%' }}>
        {runnable.length === 0 && <Alert type="info" showIcon message={t('run.noTargets')} />}

        <div>
          <Typography.Text type="secondary">{t('run.workflow')}</Typography.Text>
          <Select
            showSearch
            optionFilterProp="label"
            style={{ width: '100%', marginTop: 4 }}
            placeholder={t('run.selectWorkflow')}
            value={targetId}
            onChange={pickTarget}
            options={runnable.map((tg) => ({ value: tg.id, label: tg.name }))}
          />
        </div>

        {target && (
          <Form form={form} layout="vertical" requiredMark style={{ marginBottom: -8 }}>
            {inputs.map((i) => (
              <Form.Item
                key={i.key}
                name={i.key}
                label={i.label || i.key}
                rules={i.required ? [{ required: true, message: t('run.required', { field: i.label || i.key }) }] : []}
              >
                <Input placeholder={i.label || i.key} />
              </Form.Item>
            ))}
            {inputs.length === 0 && <Typography.Text type="secondary">{t('run.noInputs')}</Typography.Text>}
          </Form>
        )}

        <RunScheduleControls value={schedule} onChange={setSchedule} presets={enabledPresets} tickets={tickets} />

        <div>
          <span style={{ marginRight: 8 }}>{t('batch.maxRetries')}：</span>
          <InputNumber min={0} max={5} value={retries} onChange={(v) => setRetries(v ?? 0)} />
        </div>

        {mailEnabled && email && (
          <Checkbox checked={notify} onChange={(e) => setNotify(e.target.checked)}>
            {t('batch.notifyDone')}
          </Checkbox>
        )}

        <Alert type={busy ? 'warning' : 'success'} showIcon message={queueMsg} />
      </Space>
    </Modal>
  )
}
