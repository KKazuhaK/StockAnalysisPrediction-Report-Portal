import { useEffect, useMemo, useState } from 'react'
import { App, Button, Card, Checkbox, Input, InputNumber, Select, Space, Tag, Typography, Upload } from 'antd'
import { useTranslation } from 'react-i18next'
import { PlayCircleOutlined, UploadOutlined } from '@ant-design/icons'
import { api } from '../api/client'
import { useAuth } from '../auth'
import type { BatchTarget, BatchTickets, RunPreset, RunPresetsResp } from '../api/types'
import { csvToRows } from '../lib/csv'
import { BASE_MAX } from '../lib/batchUi'
import { emptySchedule, schedulePayload, scheduleError, type RunSchedule } from '../lib/runSchedule'
import RunScheduleControls from '../components/RunScheduleControls'
import QueueTable from '../components/QueueTable'

function download(name: string, text: string) {
  const blob = new Blob([text], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

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
  const [schedule, setSchedule] = useState<RunSchedule>(emptySchedule)
  const [notify, setNotify] = useState(false)
  const [csvText, setCsvText] = useState('')
  const [submitting, setSubmitting] = useState(false)

  const loadTargets = () =>
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || []))
  const loadTickets = () => api.get<BatchTickets>('/api/admin/batch/tickets').then(setTickets).catch(() => {})

  useEffect(() => {
    loadTargets()
    loadTickets()
    api
      .get<RunPresetsResp>('/api/admin/batch/presets')
      .then((r) => {
        setPresets(r.presets || [])
        setSchedule((s) => ({ ...s, mode: r.default_mode || 'now', idle: !!r.default_idle }))
      })
      .catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const urgentEnabled = tickets?.urgent_enabled !== false
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0
  useEffect(() => {
    if ((!urgentEnabled || urgentDisabled) && schedule.urgent) setSchedule((s) => ({ ...s, urgent: false }))
  }, [urgentEnabled, urgentDisabled, schedule.urgent])

  const enabledPresets = useMemo(() => presets.filter((p) => p.enabled), [presets])
  const target = useMemo(() => targets.find((tg) => tg.id === targetId), [targets, targetId])
  const inputKeys = useMemo(() => (target?.inputs || []).map((i) => i.key), [target])
  const rows = useMemo(() => (inputKeys.length ? csvToRows(csvText, inputKeys) : []), [csvText, inputKeys])

  const readFile = (file: File) => {
    const reader = new FileReader()
    reader.onload = () => setCsvText(String(reader.result || ''))
    reader.readAsText(file)
    return false
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
      setCsvText('')
      setSchedule(emptySchedule)
      setNotify(false)
      loadTickets() // an urgent run may have spent a ticket; the embedded queue self-refreshes
    } catch (e) {
      message.error((e as Error).message || t('batch.msg.startFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <PlayCircleOutlined />
            {t('batch.runTitle')}
          </Space>
        }
      >
        {targets.length === 0 ? (
          <Typography.Text type="secondary">{t('batch.noTargets')}</Typography.Text>
        ) : (
          <Space direction="vertical" size={12} style={{ width: '100%' }}>
            <Space wrap>
              <span>{t('batch.target')}：</span>
              <Select
                style={{ minWidth: 280 }}
                placeholder={t('batch.selectTarget')}
                value={targetId}
                onChange={setTargetId}
                options={targets.map((tg) => ({
                  value: tg.id,
                  label: tg.plugin_name ? `${tg.name}（${tg.plugin_name}）` : tg.name,
                }))}
              />
              <span>{t('batch.maxRetries')}：</span>
              <InputNumber min={0} max={5} value={maxRetries} onChange={(v) => setMaxRetries(v ?? 0)} />
              <span>{t('batch.rowConcurrency')}：</span>
              <InputNumber min={1} max={20} value={rowConcurrency} onChange={(v) => setRowConcurrency(v ?? 1)} />
              {admin && (
                <>
                  <span>{t('batch.priorityLabel')}：</span>
                  <InputNumber
                    min={0}
                    max={BASE_MAX}
                    value={basePriority}
                    onChange={(v) => setBasePriority(v ?? 50)}
                    disabled={schedule.urgent || schedule.idle}
                  />
                </>
              )}
            </Space>
            {target && (
              <div>
                <Typography.Text type="secondary">{t('batch.csvHeaderHint')}</Typography.Text>{' '}
                {inputKeys.map((k) => (
                  <Tag key={k}>{k}</Tag>
                ))}
                <Button type="link" size="small" onClick={() => download('template.csv', inputKeys.join(',') + '\n')}>
                  {t('batch.downloadTemplate')}
                </Button>
              </div>
            )}
            <Input.TextArea
              rows={6}
              value={csvText}
              onChange={(e) => setCsvText(e.target.value)}
              placeholder={inputKeys.length ? t('batch.csvPlaceholder', { keys: inputKeys.join(',') }) : t('batch.selectTargetFirst')}
            />
            <Space wrap>
              <Upload accept=".csv,.txt" showUploadList={false} beforeUpload={readFile}>
                <Button icon={<UploadOutlined />}>{t('batch.uploadCsv')}</Button>
              </Upload>
              <Typography.Text type="secondary">{t('batch.parsedRows', { n: rows.length })}</Typography.Text>
            </Space>
            <RunScheduleControls value={schedule} onChange={setSchedule} presets={enabledPresets} tickets={tickets} />
            <Space wrap>
              {mailEnabled && email && (
                <Checkbox checked={notify} onChange={(e) => setNotify(e.target.checked)}>
                  {t('batch.notifyDone')}
                </Checkbox>
              )}
              <Button
                type="primary"
                icon={<PlayCircleOutlined />}
                loading={submitting}
                disabled={!targetId || rows.length === 0}
                onClick={run}
              >
                {schedule.mode === 'now' ? t('batch.run') : t('run.schedule')}
              </Button>
            </Space>
          </Space>
        )}
      </Card>

      {/* The full run queue (same table + actions as the Run/queue page). */}
      <QueueTable />
    </Space>
  )
}
