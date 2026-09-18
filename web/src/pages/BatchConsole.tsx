import { useEffect, useMemo, useState } from 'react'
import { Alert, App, Button, Card, Checkbox, InputNumber, Select, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { PlayCircleOutlined } from '@ant-design/icons'
import { api, errText } from '../api/client'
import { useAuth } from '../auth'
import type { BatchTarget, BatchTickets, RunPreset, RunPresetsResp } from '../api/types'
import { BASE_MAX, visibleOn } from '../lib/batchUi'
import { activeBatchRows, blankBatchRow, type BatchDraftRow } from '../lib/batchRows'
import {
  noRunDefaults,
  readRunDefaults,
  scheduleFromDefaults,
  schedulePayload,
  scheduleError,
  type RunFormDefaults,
  type RunSchedule,
} from '../lib/runSchedule'
import RunScheduleControls from '../components/RunScheduleControls'
import QueueTable from '../components/QueueTable'
import LoadGate from '../components/LoadGate'
import BatchRowsEditor from '../components/BatchRowsEditor'

export default function BatchConsole() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { admin, email, mailEnabled } = useAuth()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [targetId, setTargetId] = useState<number | undefined>()
  const [maxRetries, setMaxRetries] = useState(2)
  const [rowConcurrency, setRowConcurrency] = useState(1)
  const [basePriority, setBasePriority] = useState(50)
  const [tickets, setTickets] = useState<BatchTickets | null>(null)
  const [presets, setPresets] = useState<RunPreset[]>([])
  const [defaults, setDefaults] = useState<RunFormDefaults>(noRunDefaults)
  const [schedule, setSchedule] = useState<RunSchedule>(scheduleFromDefaults(noRunDefaults))
  const [notify, setNotify] = useState(false)
  const [draftRows, setDraftRows] = useState<BatchDraftRow[]>([])
  const [editorValid, setEditorValid] = useState(false)
  const [editorReset, setEditorReset] = useState(0)
  const [submitting, setSubmitting] = useState(false)
  // The form knows nothing until the workflow list and the run defaults are in — same gate the
  // single-run dialog took in b32be03, for the same reason: "no runnable targets" is a statement
  // about the server, and an empty picker under it says it again more quietly.
  const [loading, setLoading] = useState(true)
  const [loadErr, setLoadErr] = useState('')

  const loadTargets = () =>
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(visibleOn(r.targets || [], 'batch')))
  const loadTickets = () => api.get<BatchTickets>('/api/admin/batch/tickets').then(setTickets).catch(() => {})

  const load = () => {
    setLoading(true)
    setLoadErr('')
    loadTickets() // ticket counts only add detail to controls that read correctly without them
    // The catch is attached here rather than at the await below: a rejection with no handler in
    // this turn is an unhandled-rejection report, however carefully it is awaited afterwards.
    const presetsRequest = api
      .get<RunPresetsResp>('/api/admin/batch/presets')
      .then((r) => {
        setPresets(r.presets || [])
        // The admin's run-form defaults (mode / window / idle). The workflow, retry and notify
        // defaults are the single-run dialog's — a batch picks its target with its CSV columns and
        // carries its own retry count, so pre-filling those here would fight the operator.
        const d = readRunDefaults(r)
        setDefaults(d)
        setSchedule((s) => ({ ...s, mode: d.mode, presetId: s.presetId ?? d.presetId, idle: d.idle }))
      })
      .catch(() => {})
    // The targets call decides whether this console has anything to say at all, so its failure is
    // the one worth reporting; a failed presets call only costs the preset windows.
    return loadTargets()
      .catch((e) => setLoadErr(errText(e, t)))
      .then(() => presetsRequest)
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const urgentEnabled = tickets?.urgent_enabled !== false
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0
  useEffect(() => {
    if ((!urgentEnabled || urgentDisabled) && schedule.urgent) setSchedule((s) => ({ ...s, urgent: false }))
  }, [urgentEnabled, urgentDisabled, schedule.urgent])

  const enabledPresets = useMemo(() => presets.filter((p) => p.enabled), [presets])
  const target = useMemo(() => targets.find((tg) => tg.id === targetId), [targets, targetId])
  const rows = useMemo(() => activeBatchRows(draftRows), [draftRows])

  // Keep values whose keys exist on the next workflow, which makes correcting an accidental
  // target choice cheap without ever assigning a value to a different column by position.
  const pickTarget = (v: number | undefined) => {
    const nextInputs = targets.find((tg) => tg.id === v)?.inputs || []
    setDraftRows((current) => {
      const filled = activeBatchRows(current)
      if (filled.length === 0) return [blankBatchRow(nextInputs)]
      return filled.map((row) => Object.fromEntries(nextInputs.map((input) => [input.key, row[input.key] ?? ''])))
    })
    setEditorValid(false)
    setTargetId(v)
  }

  const run = async () => {
    if (!targetId || rows.length === 0) return
    const err = scheduleError(schedule)
    if (err) {
      message.error(t(err))
      return
    }
    setSubmitting(true)
    try {
      const sp = schedulePayload(schedule)
      // Per-batch row concurrency chosen here (default 1); the backend caps it at the global
      // "max at once" budget so a batch can't overrun the queue. urgent/idle win the priority
      // lane; else an admin may set a base number; else the backend resolves the group default.
      const res = await api.post<{ job_id: number; concurrency: number; downgraded?: boolean; run_at?: string }>('/api/admin/batch/jobs', {
        target_id: targetId,
        concurrency: rowConcurrency,
        max_retries: maxRetries,
        priority: sp.priority || (admin ? String(basePriority) : ''),
        run_at: sp.run_at,
        preset_id: sp.preset_id,
        notify,
        rows,
      })
      if (res.run_at) message.success(t('run.scheduledOk', { at: res.run_at }))
      else message.success(t('batch.msg.started', { id: res.job_id, n: rows.length }))
      if (res.downgraded) message.warning(t('batch.ticketDowngraded'))
      setDraftRows([blankBatchRow(target?.inputs || [])])
      setEditorValid(false)
      setEditorReset((value) => value + 1)
      setSchedule(scheduleFromDefaults(defaults))
      setNotify(false)
      loadTickets() // an urgent run may have spent a ticket; the embedded queue self-refreshes
    } catch (e) {
      message.error(errText(e, t) || t('batch.msg.startFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <PlayCircleOutlined />
            {t('batch.runTitle')}
          </Space>
        }
      >
        <LoadGate loading={loading} error={loadErr} onRetry={load} minHeight={200} title={t('common.loadFailedContent')}>
          {targets.length === 0 ? (
            <Typography.Text type="secondary">{t('batch.noTargets')}</Typography.Text>
          ) : (
            <div className="rp-batch-compose-layout">
              <section className="rp-batch-compose-data">
                <Typography.Title level={5} className="rp-batch-compose-title">
                  {t('batch.editor.dataTitle')}
                </Typography.Title>
                <div className="rp-batch-target-picker">
                  <Typography.Text>{t('batch.target')}</Typography.Text>
                  <Select
                    showSearch
                    optionFilterProp="label"
                    placeholder={t('batch.selectTarget')}
                    value={targetId}
                    onChange={pickTarget}
                    options={targets.map((tg) => ({
                      value: tg.id,
                      label: tg.plugin_name ? `${tg.name}（${tg.plugin_name}）` : tg.name,
                    }))}
                  />
                </div>
                {target ? (
                  <BatchRowsEditor
                    key={`${target.id}:${editorReset}`}
                    inputs={target.inputs || []}
                    rows={draftRows}
                    onChange={setDraftRows}
                    onValidityChange={setEditorValid}
                  />
                ) : (
                  <div className="rp-batch-editor-empty">
                    <Typography.Text type="secondary">{t('batch.selectTargetFirst')}</Typography.Text>
                  </div>
                )}
              </section>

              <aside className="rp-batch-compose-settings">
                <Typography.Title level={5} className="rp-batch-compose-title">
                  {t('run.settings')}
                </Typography.Title>
                <div className="rp-batch-setting-fields">
                  <label>
                    <Typography.Text>{t('batch.maxRetries')}</Typography.Text>
                    <InputNumber min={0} max={5} value={maxRetries} onChange={(v) => setMaxRetries(v ?? 0)} />
                  </label>
                  <label>
                    <Typography.Text>{t('batch.rowConcurrency')}</Typography.Text>
                    <InputNumber min={1} max={20} value={rowConcurrency} onChange={(v) => setRowConcurrency(v ?? 1)} />
                  </label>
                  {admin && (
                    <label>
                      <Typography.Text>{t('batch.priorityLabel')}</Typography.Text>
                      <InputNumber
                        min={0}
                        max={BASE_MAX}
                        value={basePriority}
                        onChange={(v) => setBasePriority(v ?? 50)}
                        disabled={schedule.urgent || schedule.idle}
                      />
                    </label>
                  )}
                </div>

                <RunScheduleControls
                  value={schedule}
                  onChange={setSchedule}
                  presets={enabledPresets}
                  tickets={tickets}
                  showRule={defaults.showPresetRule}
                />

                {mailEnabled && email && (
                  <Checkbox checked={notify} onChange={(e) => setNotify(e.target.checked)}>
                    {t('batch.notifyDone')}
                  </Checkbox>
                )}

                <Alert
                  type={editorValid ? 'success' : 'info'}
                  showIcon
                  title={editorValid ? t('batch.editor.submitReady', { n: rows.length }) : t('batch.editor.submitBlocked')}
                />

                <Button
                  block
                  type="primary"
                  size="large"
                  icon={<PlayCircleOutlined />}
                  loading={submitting}
                  disabled={!targetId || !editorValid}
                  onClick={run}
                >
                  {schedule.mode === 'now' ? t('batch.run') : t('run.schedule')}
                </Button>
              </aside>
            </div>
          )}
        </LoadGate>
      </Card>

      {/* The full run queue (same table + actions as the Run/queue page). */}
      <QueueTable />
    </Space>
  )
}
