import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, App, Checkbox, Form, Input, InputNumber, Modal, Select, Space, Spin, Typography } from 'antd'
import { PlayCircleOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { api, errText } from '../api/client'
import { useAuth } from '../auth'
import { visibleOn } from '../lib/batchUi'
import { buildRow, isFileInput } from '../lib/difyInputs'
import { readPrefetched } from '../lib/prefetch'
import {
  noRunDefaults,
  readRunDefaults,
  scheduleFromDefaults,
  schedulePayload,
  scheduleError,
  type RunFormDefaults,
  type RunSchedule,
} from '../lib/runSchedule'
import DifyFileInput from './DifyFileInput'
import RunScheduleControls from './RunScheduleControls'
import type { BatchQueueSummary, BatchTarget, BatchTickets, PluginInput, RunPreset, RunPresetsResp, RunQuota } from '../api/types'

// The home-page run-analysis modal (docs/adr/0007 + 0014): pick a Dify workflow, fill its
// discovered inputs, choose when to run (now / a preset low-peak window / an explicit 定时 time)
// and the priority lane (加急 / 队列空闲), with the live queue depth shown inline. The run-time +
// priority controls are the shared RunScheduleControls, reused by the batch console.
// An unknown period reads as the daily one: that is what every pre-period deployment meant, and it
// is the phrasing that overstates the allowance least.
const quotaPeriod = (p?: string) => (p === 'week' || p === 'month' || p === 'total' ? p : 'day')

// inputControl draws one declared input as the control its type calls for. Everything unrecognised
// falls back to a text box — the one control that can carry any of these types as text, and what
// the form drew for every input before the declaration carried a type at all. `select` needs its
// allowed values to be a Select at all, so a select that arrives without options falls back too,
// leaving the field fillable instead of offering an empty menu.
function inputControl(i: PluginInput, targetId: number) {
  const hint = i.label || i.key
  switch (i.type) {
    case 'paragraph':
      return <Input.TextArea autoSize={{ minRows: 2, maxRows: 6 }} placeholder={hint} />
    case 'number':
      return <InputNumber style={{ width: '100%' }} placeholder={hint} />
    case 'select':
      if (i.options?.length) {
        return <Select placeholder={hint} options={i.options.map((o) => ({ value: o, label: o }))} />
      }
      return <Input placeholder={hint} />
    case 'file':
    case 'file-list':
      return <DifyFileInput targetId={targetId} type={i.type} />
    default:
      return <Input placeholder={hint} />
  }
}

// How stale a warmed answer may be and still open the dialog without waiting. The live request
// goes out regardless and corrects whatever this showed, so the number only bounds how long a
// deleted workflow can stay on screen before the answer lands.
const WARM_MAX_AGE = 5 * 60_000

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
  const navigate = useNavigate()
  const [form] = Form.useForm()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [targetId, setTargetId] = useState<number | undefined>()
  const [tickets, setTickets] = useState<BatchTickets | null>(null)
  const [quota, setQuota] = useState<RunQuota | null>(null)
  const [queue, setQueue] = useState<BatchQueueSummary | null>(null)
  const [presets, setPresets] = useState<RunPreset[]>([])
  const [defaults, setDefaults] = useState<RunFormDefaults>(noRunDefaults)
  const [schedule, setSchedule] = useState<RunSchedule>(scheduleFromDefaults(noRunDefaults))
  const [notify, setNotify] = useState(false)
  const [retries, setRetries] = useState(0) // failure retries; 0 = never auto-retry (a single run maps 1:1 to the click)
  const [submitting, setSubmitting] = useState(false)
  const [loading, setLoading] = useState(true) // until the workflow list is in, this modal knows nothing
  const [targetsOk, setTargetsOk] = useState(false) // …and a failed list is not an empty one
  const [queueSettled, setQueueSettled] = useState(false) // the ungated queue-depth call has come back
  const [loadErr, setLoadErr] = useState('')

  // Presets carry the admin's run-form defaults with them, so reading them warm and reading them
  // live must apply the same rules — hence one function, used by both. The workflow default is
  // applied separately (below), because it needs the target list this call doesn't carry.
  const applyPresets = useCallback((r: RunPresetsResp) => {
    setPresets(r.presets || [])
    const d = readRunDefaults(r)
    setDefaults(d)
    setSchedule((s) => ({ ...s, mode: d.mode, presetId: s.presetId ?? d.presetId, idle: d.idle }))
    setRetries(d.retries)
    // The done-email is only offered to a user who has one on a portal that can send it; a default
    // of "on" must not post notify for everyone else, whose checkbox isn't even drawn.
    setNotify(d.notify && mailEnabled && !!email)
  }, [mailEnabled, email])

  useEffect(() => {
    if (!open) return
    // The form is meaningless until the workflow list and the run defaults are in: an empty
    // Select reads as "nothing to run" and the no-targets notice states it outright, which on a
    // slow link is a claim about the server that has not answered yet. Both of those wait behind
    // `loading`. The three secondary calls (tickets, quota, queue depth) only add detail to
    // controls that render sensibly without them, so they are not part of the gate.
    // The shell warms these two while it is idle (AppLayout), so most opens have them already.
    // A warm answer decides only whether this dialog waits: the requests below go out either way
    // and overwrite it, so a workflow added in another tab a minute ago still appears.
    const warmTargets = readPrefetched<{ targets: BatchTarget[] }>('/api/admin/batch/targets', WARM_MAX_AGE)
    const warmPresets = readPrefetched<RunPresetsResp>('/api/admin/batch/presets', WARM_MAX_AGE)
    setLoadErr('')
    if (warmTargets && warmPresets) {
      setTargets(warmTargets.targets || [])
      applyPresets(warmPresets)
      setTargetsOk(true)
      setLoading(false)
    } else {
      setTargetsOk(false)
      setLoading(true)
    }
    api.get<BatchTickets>('/api/admin/batch/tickets').then(setTickets).catch(() => {})
    api.get<RunQuota>('/api/admin/batch/run-quota').then(setQuota).catch(() => {})
    setQueueSettled(false)
    api
      .get<BatchQueueSummary>('/api/admin/batch/queue')
      .then(setQueue)
      .catch(() => {})
      // Settled either way: this call is outside the gate, so a failure has to be able to end the
      // banner's "Loading…" — it just ends it by saying nothing rather than by guessing.
      .finally(() => setQueueSettled(true))
    let live = true
    const gated = [
      api
        .get<{ targets: BatchTarget[] }>('/api/admin/batch/targets')
        .then((r) => {
          setTargets(r.targets || [])
          setTargetsOk(true)
        })
        // allSettled below swallows this, and the spinner would end on "No workflows configured
        // yet. Add one under Manage → Batch." — advice for a server that never answered.
        .catch((e) => {
          if (live) setLoadErr(errText(e, t))
        }),
      // Presets + the admin-set run-form defaults (default mode button + idle pre-check).
      api.get<RunPresetsResp>('/api/admin/batch/presets').then(applyPresets),
    ]
    // allSettled, not all: a failed presets call must still open the form (with no preset
    // windows), rather than leave the modal spinning for ever on a detail.
    Promise.allSettled(gated).then(() => {
      if (live) setLoading(false)
    })
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  // 运行分析 generates a report (Dify ingests it). visibleOn applies both rules: agent apps
  // are conversational and post no report (capability), and an admin may restrict a target
  // to other surfaces (policy). The capability half used to be an inline filter here — it
  // moved to batchUi so the four surfaces cannot drift apart.
  const runnable = useMemo(() => visibleOn(targets, 'run'), [targets])
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

  // …and failing that, the admin's default workflow, once the list it must be in has arrived. A
  // shortcut's own workflow wins (it is the whole point of the button), and so does a choice the
  // user has already made — this only ever fills an empty picker. A default naming a workflow that
  // is gone, or one an admin has hidden from 运行分析, simply doesn't apply.
  useEffect(() => {
    if (!open || initialTargetId != null || targetId != null || !defaults.targetId) return
    if (runnable.some((tg) => tg.id === defaults.targetId)) {
      setTargetId(defaults.targetId)
      form.resetFields()
    }
  }, [open, initialTargetId, targetId, defaults.targetId, runnable])

  const urgentEnabled = tickets?.urgent_enabled !== false
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0
  useEffect(() => {
    if ((!urgentEnabled || urgentDisabled) && schedule.urgent) setSchedule((s) => ({ ...s, urgent: false }))
  }, [urgentEnabled, urgentDisabled, schedule.urgent])

  const pickTarget = (id: number) => {
    setTargetId(id)
    form.resetFields()
  }

  // After a submit the form returns to the state it opened in — the admin's defaults — not to
  // blank. Clearing the workflow lets the default-workflow effect above re-fill it.
  const reset = () => {
    setTargetId(undefined)
    setNotify(defaults.notify && mailEnabled && !!email)
    setRetries(defaults.retries)
    setSchedule(scheduleFromDefaults(defaults))
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
      const row = buildRow(inputs, vals)
      const sp = schedulePayload(schedule)
      const res = await api.post<{
        job_id: number
        downgraded?: boolean
        run_at?: string
        reused?: boolean
        report_id?: number
        key?: string
      }>(
        '/api/admin/batch/jobs',
        {
          target_id: targetId,
          concurrency: 1,
          max_retries: retries, // default 0 (no auto-retry); user can opt into failure retries, same as batch
          priority: sp.priority, // "urgent" | "idle" | "" (backend resolves the "" default)
          run_at: sp.run_at,
          preset_id: sp.preset_id,
          surface: 'run',
          notify,
          rows: [row],
        },
      )
      // Already generated today (ADR 0022 R1): no run happened — open the existing report instead
      // of reporting a job that does not exist.
      if (res.reused && res.report_id && res.key) {
        message.success(t('run.reusedToday'))
        reset()
        onClose()
        navigate(`/run/${encodeURIComponent(res.key)}?r=${res.report_id}`)
        return
      }
      if (res.run_at) message.success(t('run.scheduledOk', { at: res.run_at }))
      else message.success(t('run.startedOk', { id: res.job_id }))
      if (res.downgraded) message.warning(t('batch.ticketDowngraded'))
      onSubmitted?.(res.job_id)
      reset()
      onClose()
    } catch (e) {
      message.error(errText(e, t) || t('run.startFailed'))
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
  // The queue call stays outside the modal's gate (it only annotates controls that read fine
  // without it), so this banner must be able to say nothing. Coercing a pending answer to zero
  // put "Queue idle — runs immediately." in green on top of a queue nobody had asked about.
  const queueMsg = !queue
    ? t('common.loading')
    : busy
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
      okButtonProps={{ loading: submitting, disabled: loading || !targetId }}
      cancelText={t('common.cancel')}
      onCancel={onClose}
      destroyOnHidden
    >
      {loading ? (
        // A spinner is the honest answer while the workflow list is on the wire. What used to be
        // here — an empty Select under "no workflows are configured" — described a server that
        // had not spoken yet.
        <div style={{ display: 'grid', alignContent: 'center', justifyItems: 'center', minHeight: 180, gap: 12 }}>
          <Spin size="large" />
          <Typography.Text type="secondary">{t('run.loading')}</Typography.Text>
        </div>
      ) : !targetsOk && loadErr ? (
        <Alert type="error" showIcon message={t('common.loadFailedContent')} description={loadErr} />
      ) : (
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
                // A file field holds a list of uploaded files, and the rule has to say so: a rule
                // with no type validates as a string, which rejects the list outright — a required
                // file input would then refuse every value, uploaded file included.
                rules={
                  i.required
                    ? [
                        {
                          required: true,
                          type: isFileInput(i.type) ? 'array' : undefined,
                          message: t('run.required', { field: i.label || i.key }),
                        },
                      ]
                    : []
                }
              >
                {inputControl(i, target.id)}
              </Form.Item>
            ))}
            {inputs.length === 0 && <Typography.Text type="secondary">{t('run.noInputs')}</Typography.Text>}
          </Form>
        )}

        {/* Run quota (ADR 0022 R2): shown only to a capped (external) member, so internal users
            and admins see the form exactly as before. The window is part of the sentence — "2 of 5
            left" is a different fact depending on whether it refills tomorrow or never. */}
        {quota?.limited && (
          <Typography.Text type={(quota.remaining ?? 0) <= 0 ? 'danger' : 'secondary'}>
            {t(`run.quotaRemaining.${quotaPeriod(quota.period)}`, {
              remaining: quota.remaining ?? 0,
              limit: quota.limit ?? 0,
            })}
          </Typography.Text>
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

        {/* Nothing at all if the depth never arrived: the banner exists to describe the queue,
            and with no answer it has nothing to describe. */}
        {(queue || !queueSettled) && <Alert type={!queue ? 'info' : busy ? 'warning' : 'success'} showIcon message={queueMsg} />}
      </Space>
      )}
    </Modal>
  )
}
