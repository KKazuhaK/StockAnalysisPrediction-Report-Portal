import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Button,
  Card,
  Checkbox,
  Drawer,
  Input,
  InputNumber,
  Progress,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  Upload,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import {
  DownloadOutlined,
  EyeOutlined,
  ReloadOutlined,
  StopOutlined,
  ThunderboltOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { api } from '../api/client'
import type { BatchItem, BatchJob, BatchJobDetail, BatchTarget, BatchTickets } from '../api/types'
import { csvToRows, toCSV } from '../lib/csv'
import { BASE_MAX, isUrgent, priorityNum, priorityTag } from '../lib/batchUi'

const ITEM_STATUS_COLOR: Record<string, string> = {
  queued: 'default',
  running: 'processing',
  succeeded: 'success',
  partial: 'warning',
  failed: 'error',
}
const JOB_STATUS_COLOR: Record<string, string> = {
  queued: 'default',
  running: 'processing',
  cancelling: 'warning',
  cancelled: 'default',
  finished: 'success',
}

// priorityTag / priorityNum / isUrgent / BASE_MAX come from lib/batchUi (ADR 0008).

function statusTag(t: TFunction, s: string, colors: Record<string, string>) {
  return <Tag color={colors[s] || 'default'}>{t(`batch.status.${s}`)}</Tag>
}

function isTerminal(status: string) {
  return status === 'finished' || status === 'cancelled'
}

function download(name: string, text: string) {
  const blob = new Blob([text], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

function fmtInputs(s: string) {
  try {
    const o = JSON.parse(s) as Record<string, string>
    return Object.entries(o)
      .map(([k, v]) => `${k}=${v}`)
      .join('  ')
  } catch {
    return s
  }
}

function JobProgress({ job }: { job: BatchJob }) {
  const { t } = useTranslation()
  const done = job.succeeded + job.partial + job.failed
  const pct = job.total ? Math.round((done / job.total) * 100) : 0
  return (
    <Space size={6}>
      <Progress percent={pct} size="small" style={{ width: 110 }} status={job.failed ? 'exception' : undefined} />
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t('batch.progressText', { done, total: job.total, ok: job.succeeded, fail: job.failed, partial: job.partial })}
      </Typography.Text>
    </Space>
  )
}

function JobDrawer({
  jobId,
  onClose,
  onChanged,
}: {
  jobId: number | null
  onClose: () => void
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [detail, setDetail] = useState<BatchJobDetail | null>(null)
  const open = jobId != null

  const load = () => {
    if (jobId != null) api.get<BatchJobDetail>(`/api/admin/batch/jobs/${jobId}`).then(setDetail).catch(() => {})
  }
  useEffect(() => {
    if (open) {
      setDetail(null)
      load()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId])
  useEffect(() => {
    if (!open) return
    const running = detail == null || !isTerminal(detail.job.status)
    if (!running) return
    const id = setInterval(load, 2000)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId, detail?.job.status])

  const cancel = async () => {
    await api.post(`/api/admin/batch/jobs/${jobId}/cancel`)
    message.success(t('batch.msg.cancelRequested'))
    load()
    onChanged()
  }
  const retry = async () => {
    const r = await api.post<{ requeued: number }>(`/api/admin/batch/jobs/${jobId}/retry`, { statuses: ['failed'] })
    message.success(t('batch.msg.requeued', { n: r.requeued }))
    load()
    onChanged()
  }
  const exportCsv = () => {
    if (!detail) return
    const rows = detail.items.map((it) => [it.row_index, it.inputs, it.status, it.attempts, it.run_id, it.error])
    const headers = [
      t('batch.col.row'),
      t('batch.col.inputs'),
      t('batch.col.status'),
      t('batch.col.attempts'),
      t('batch.col.runId'),
      t('batch.col.error'),
    ]
    download(`batch-${jobId}.csv`, toCSV(headers, rows))
  }

  const job = detail?.job
  const c = detail?.counts
  const itemCols: ColumnsType<BatchItem> = [
    { title: t('batch.col.row'), dataIndex: 'row_index', width: 56 },
    { title: t('batch.col.inputs'), dataIndex: 'inputs', render: (s: string) => <span style={{ fontSize: 12 }}>{fmtInputs(s)}</span> },
    { title: t('batch.col.status'), dataIndex: 'status', width: 100, render: (s: string) => statusTag(t, s, ITEM_STATUS_COLOR) },
    { title: t('batch.col.attempts'), dataIndex: 'attempts', width: 60 },
    {
      title: t('batch.col.error'),
      dataIndex: 'error',
      render: (e: string) =>
        e ? (
          <Typography.Text type="danger" style={{ fontSize: 12 }}>
            {e}
          </Typography.Text>
        ) : (
          ''
        ),
    },
  ]

  return (
    <Drawer title={job ? t('batch.jobTitle', { id: job.id }) : t('batch.jobDetail')} width={720} open={open} onClose={onClose} destroyOnClose>
      {job && c && (
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Space wrap>
            {statusTag(t, job.status, JOB_STATUS_COLOR)}
            {priorityTag(t, job.priority)}
            {job.status === 'queued' && (
              <Typography.Text type="secondary">
                {job.ahead ? t('batch.aheadN', { n: job.ahead }) : t('batch.aheadNext')}
              </Typography.Text>
            )}
            <Typography.Text type="secondary">
              {t('batch.countsFull', {
                total: job.total,
                queued: c.queued,
                running: c.running,
                succeeded: c.succeeded,
                partial: c.partial,
                failed: c.failed,
              })}
            </Typography.Text>
          </Space>
          <Space wrap>
            {!isTerminal(job.status) && (
              <Button danger icon={<StopOutlined />} onClick={cancel}>
                {t('common.cancel')}
              </Button>
            )}
            {c.failed > 0 && (
              <Button icon={<ReloadOutlined />} onClick={retry}>
                {t('batch.retryFailed', { n: c.failed })}
              </Button>
            )}
            <Button icon={<DownloadOutlined />} onClick={exportCsv}>
              {t('batch.exportCsv')}
            </Button>
          </Space>
          <Table rowKey="id" size="small" dataSource={detail.items} columns={itemCols} pagination={{ pageSize: 20, size: 'small' }} />
        </Space>
      )}
    </Drawer>
  )
}

export default function BatchConsole() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [targetId, setTargetId] = useState<number | undefined>()
  const [maxRetries, setMaxRetries] = useState(2)
  const [urgent, setUrgent] = useState(false)
  const [basePriority, setBasePriority] = useState(50)
  const [tickets, setTickets] = useState<BatchTickets | null>(null)
  const [csvText, setCsvText] = useState('')
  const [jobs, setJobs] = useState<BatchJob[]>([])
  const [openJobId, setOpenJobId] = useState<number | null>(null)
  const [submitting, setSubmitting] = useState(false)

  const loadTargets = () =>
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || []))
  const loadJobs = () => api.get<{ jobs: BatchJob[] }>('/api/admin/batch/jobs').then((r) => setJobs(r.jobs || []))
  const loadTickets = () => api.get<BatchTickets>('/api/admin/batch/tickets').then(setTickets).catch(() => {})

  useEffect(() => {
    loadTargets()
    loadJobs()
    loadTickets()
    const id = setInterval(() => loadJobs().catch(() => {}), 3000)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const urgentEnabled = tickets?.urgent_enabled !== false
  // 加急 needs a ticket (unless unlimited/admin); disable it when the balance is 0.
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0
  useEffect(() => {
    if ((!urgentEnabled || urgentDisabled) && urgent) setUrgent(false)
  }, [urgentEnabled, urgentDisabled, urgent])

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
    setSubmitting(true)
    try {
      // Batch rows run sequentially (concurrency 1) — the per-run concurrency knob was removed.
      const res = await api.post<{ job_id: number; concurrency: number; downgraded?: boolean }>('/api/admin/batch/jobs', {
        target_id: targetId,
        concurrency: 1,
        max_retries: maxRetries,
        priority: urgent ? 'urgent' : String(basePriority),
        rows,
      })
      message.success(t('batch.msg.started', { id: res.job_id, n: rows.length }))
      if (res.downgraded) message.warning(t('batch.ticketDowngraded'))
      setCsvText('')
      loadJobs()
      loadTickets() // a 加急 run may have spent a ticket
      setOpenJobId(res.job_id)
    } catch (e) {
      message.error((e as Error).message || t('batch.msg.startFailed'))
    } finally {
      setSubmitting(false)
    }
  }

  const reprioritize = async (id: number, p: string) => {
    await api.post(`/api/admin/batch/jobs/${id}/priority`, { priority: p })
    loadJobs()
  }

  const jobCols: ColumnsType<BatchJob> = [
    { title: '#', dataIndex: 'id', width: 64 },
    { title: t('batch.col.status'), dataIndex: 'status', width: 96, render: (s: string) => statusTag(t, s, JOB_STATUS_COLOR) },
    {
      // Queued non-urgent jobs get an inline base-priority editor (插队); 加急 and
      // non-queued jobs show a static tag (ADR 0008).
      title: t('batch.priorityLabel'),
      width: 116,
      render: (_: unknown, j: BatchJob) =>
        j.status === 'queued' && !isUrgent(j.priority) ? (
          <InputNumber
            size="small"
            min={0}
            max={BASE_MAX}
            style={{ width: 76 }}
            value={priorityNum(j.priority)}
            onChange={(v) => reprioritize(j.id, String(v ?? 50))}
          />
        ) : (
          priorityTag(t, j.priority)
        ),
    },
    {
      title: t('batch.col.progress'),
      width: 240,
      render: (_: unknown, j: BatchJob) =>
        j.status === 'queued' ? (
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {j.ahead ? t('batch.aheadN', { n: j.ahead }) : t('batch.aheadNext')}
          </Typography.Text>
        ) : (
          <JobProgress job={j} />
        ),
    },
    { title: t('batch.col.createdBy'), dataIndex: 'created_by', width: 110 },
    { title: t('batch.col.startedAt'), dataIndex: 'started_at', width: 170 },
    {
      title: t('batch.col.actions'),
      width: 88,
      render: (_: unknown, j: BatchJob) => (
        <Button size="small" icon={<EyeOutlined />} onClick={() => setOpenJobId(j.id)}>
          {t('batch.view')}
        </Button>
      ),
    },
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <ThunderboltOutlined />
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
              <span>{t('batch.priorityLabel')}：</span>
              <InputNumber min={0} max={BASE_MAX} value={basePriority} onChange={(v) => setBasePriority(v ?? 50)} disabled={urgent} />
              {urgentEnabled && (
                <>
                  <Checkbox checked={urgent} disabled={urgentDisabled} onChange={(e) => setUrgent(e.target.checked)}>
                    {t('run.urgent')}
                  </Checkbox>
                  {tickets && !tickets.unlimited && (
                    <Tag color={(tickets.remaining ?? 0) > 0 ? 'gold' : 'default'} icon={<ThunderboltOutlined />}>
                      {t('batch.ticketsLeft', { n: tickets.remaining ?? 0, total: tickets.allocation ?? 0 })}
                    </Tag>
                  )}
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
              <Button
                type="primary"
                icon={<ThunderboltOutlined />}
                loading={submitting}
                disabled={!targetId || rows.length === 0}
                onClick={run}
              >
                {t('batch.run')}
              </Button>
            </Space>
          </Space>
        )}
      </Card>

      <Card title={t('batch.jobsTitle')}>
        <Table rowKey="id" size="small" dataSource={jobs} columns={jobCols} pagination={{ pageSize: 10 }} />
      </Card>

      <JobDrawer jobId={openJobId} onClose={() => setOpenJobId(null)} onChanged={loadJobs} />
    </Space>
  )
}
