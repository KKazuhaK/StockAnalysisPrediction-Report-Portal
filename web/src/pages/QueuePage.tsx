import { useEffect, useMemo, useState } from 'react'
import {
  App,
  Button,
  Card,
  DatePicker,
  Drawer,
  Empty,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Progress,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  ClockCircleOutlined,
  DeleteOutlined,
  EyeOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  StopOutlined,
} from '@ant-design/icons'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { Dayjs } from 'dayjs'
import { api } from '../api/client'
import { useAuth } from '../auth'
import type { BatchItem, BatchJob, BatchJobDetail, BatchQueueSummary, BatchTarget } from '../api/types'
import { BASE_MAX, fmtInputs, isTerminal, isUrgent, priorityNum, priorityTag, statusTag } from '../lib/batchUi'

const todayStr = () => new Date().toISOString().slice(0, 10)

// Detail drawer: a run's rows, inputs, errors + a link to the stock's reports.
function DetailDrawer({ jobId, onClose }: { jobId: number | null; onClose: () => void }) {
  const { t } = useTranslation()
  const [detail, setDetail] = useState<BatchJobDetail | null>(null)
  const open = jobId != null
  useEffect(() => {
    if (jobId == null) return
    setDetail(null)
    const load = () => api.get<BatchJobDetail>(`/api/admin/batch/jobs/${jobId}`).then(setDetail).catch(() => {})
    load()
    const id = setInterval(load, 2500)
    return () => clearInterval(id)
  }, [jobId])

  const symbolOf = (it: BatchItem) => {
    try {
      const o = JSON.parse(it.inputs) as Record<string, string>
      return o.symbol || o.code || ''
    } catch {
      return ''
    }
  }
  const cols: ColumnsType<BatchItem> = [
    { title: t('batch.col.row'), dataIndex: 'row_index', width: 48 },
    { title: t('batch.col.inputs'), render: (_: unknown, it) => <span style={{ fontSize: 12 }}>{fmtInputs(it.inputs)}</span> },
    { title: t('batch.col.status'), dataIndex: 'status', width: 92, render: (s: string) => statusTag(t, s) },
    {
      title: t('batch.col.error'),
      render: (_: unknown, it) =>
        it.error ? (
          <Typography.Text type="danger" style={{ fontSize: 12 }}>
            {it.error}
          </Typography.Text>
        ) : it.status === 'succeeded' && symbolOf(it) ? (
          <Link to={`/stock/${encodeURIComponent(symbolOf(it))}`}>{t('queue.viewReport')}</Link>
        ) : (
          ''
        ),
    },
  ]
  return (
    <Drawer title={detail ? t('batch.jobTitle', { id: detail.job.id }) : t('batch.jobDetail')} width={680} open={open} onClose={onClose} destroyOnClose>
      {detail && <Table rowKey="id" size="small" dataSource={detail.items} columns={cols} pagination={{ pageSize: 20, size: 'small' }} />}
    </Drawer>
  )
}

export default function QueuePage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { admin } = useAuth()
  const [jobs, setJobs] = useState<BatchJob[]>([])
  const [summary, setSummary] = useState<BatchQueueSummary | null>(null)
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [auto, setAuto] = useState(true)
  const [fStatus, setFStatus] = useState<string>('')
  const [fUser, setFUser] = useState<string>('')
  const [fTarget, setFTarget] = useState<number | undefined>()
  const [search, setSearch] = useState('')
  const [detailId, setDetailId] = useState<number | null>(null)
  const [reschedId, setReschedId] = useState<number | null>(null)
  const [reschedAt, setReschedAt] = useState<Dayjs | null>(null)

  const load = () => {
    api.get<{ jobs: BatchJob[] }>('/api/admin/batch/jobs').then((r) => setJobs(r.jobs || [])).catch(() => {})
    api.get<BatchQueueSummary>('/api/admin/batch/queue').then(setSummary).catch(() => {})
  }
  useEffect(() => {
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || [])).catch(() => {})
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    if (!auto) return
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto])

  const targetName = (id: number) => targets.find((tg) => tg.id === id)?.name || `#${id}`
  const submitters = useMemo(() => [...new Set(jobs.map((j) => j.created_by).filter(Boolean))], [jobs])
  const doneToday = useMemo(() => jobs.filter((j) => isTerminal(j.status) && (j.finished_at || '').startsWith(todayStr())).length, [jobs])

  const rows = useMemo(() => {
    const q = search.trim().toLowerCase()
    return jobs.filter((j) => {
      if (fStatus === 'scheduled' ? !j.scheduled : fStatus && j.status !== fStatus) return false
      if (fUser && j.created_by !== fUser) return false
      if (fTarget && j.target_id !== fTarget) return false
      if (q && !`${targetName(j.target_id)} ${fmtInputs(j.inputs)} ${j.created_by}`.toLowerCase().includes(q)) return false
      return true
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobs, fStatus, fUser, fTarget, search, targets])

  const act = async (fn: () => Promise<unknown>, okMsg?: string) => {
    try {
      await fn()
      if (okMsg) message.success(okMsg)
      load()
    } catch (e) {
      message.error((e as Error).message || 'failed')
    }
  }
  const cancel = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/cancel`), t('batch.msg.cancelRequested'))
  const del = (id: number) => act(() => api.del(`/api/admin/batch/jobs/${id}`), t('queue.deleted'))
  const retry = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/retry`, { statuses: ['failed'] }), t('queue.retried'))
  const runNow = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/schedule`, { run_at: '' }), t('queue.runningNow'))
  const reprioritize = (id: number, p: string) => act(() => api.post(`/api/admin/batch/jobs/${id}/priority`, { priority: p }))
  const saveReschedule = () =>
    act(
      () => api.post(`/api/admin/batch/jobs/${reschedId}/schedule`, { run_at: reschedAt ? reschedAt.format('YYYY-MM-DD HH:mm:ss') : '' }),
      t('queue.rescheduled'),
    ).then(() => { setReschedId(null); setReschedAt(null) })

  const cols: ColumnsType<BatchJob> = [
    {
      title: t('queue.colWorkflow'),
      render: (_: unknown, j) => (
        <div>
          <div style={{ fontSize: 13 }}>{targetName(j.target_id)}</div>
          {fmtInputs(j.inputs) && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {fmtInputs(j.inputs)}
            </Typography.Text>
          )}
        </div>
      ),
    },
    { title: t('batch.col.createdBy'), dataIndex: 'created_by', width: 110 },
    { title: t('queue.colSubmitted'), dataIndex: 'created_at', width: 160, render: (s: string) => <span style={{ fontSize: 12 }}>{s}</span> },
    {
      title: t('batch.col.status'),
      width: 108,
      render: (_: unknown, j) =>
        j.scheduled ? (
          <Tag icon={<ClockCircleOutlined />} color="purple" title={j.run_at}>
            {t('queue.scheduled')}
          </Tag>
        ) : (
          statusTag(t, j.status)
        ),
    },
    {
      // A queued non-urgent job gets an inline base-priority editor (插队); 加急 and
      // non-queued jobs show a static tag (ADR 0008).
      title: t('batch.priorityLabel'),
      width: 116,
      render: (_: unknown, j) =>
        admin && j.status === 'queued' && !j.scheduled && !isUrgent(j.priority) ? (
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
      title: t('queue.colProgress'),
      width: 220,
      render: (_: unknown, j) => {
        if (j.scheduled)
          return (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              <ClockCircleOutlined /> {j.run_at}
            </Typography.Text>
          )
        if (j.status === 'queued')
          return <Tag>{j.ahead ? t('batch.aheadN', { n: j.ahead }) : t('batch.aheadNext')}</Tag>
        const done = j.succeeded + j.partial + j.failed
        return <Progress percent={j.total ? Math.round((done / j.total) * 100) : 0} size="small" status={j.failed ? 'exception' : undefined} style={{ maxWidth: 200 }} />
      },
    },
    {
      title: t('batch.col.actions'),
      width: 150,
      render: (_: unknown, j) => (
        <Space size={2}>
          <Button size="small" type="text" icon={<EyeOutlined />} onClick={() => setDetailId(j.id)} title={t('batch.view')} />
          {j.scheduled && <Button size="small" type="text" icon={<PlayCircleOutlined />} onClick={() => runNow(j.id)} title={t('queue.runNow')} />}
          {j.status === 'queued' && (
            <Button size="small" type="text" icon={<ClockCircleOutlined />} onClick={() => { setReschedId(j.id); setReschedAt(null) }} title={t('queue.reschedule')} />
          )}
          {isTerminal(j.status) && j.failed > 0 && (
            <Button size="small" type="text" icon={<ReloadOutlined />} onClick={() => retry(j.id)} title={t('batch.retryFailed', { n: j.failed })} />
          )}
          {!isTerminal(j.status) ? (
            <Popconfirm title={t('queue.cancelConfirm')} onConfirm={() => cancel(j.id)}>
              <Button size="small" type="text" danger icon={<StopOutlined />} title={t('common.cancel')} />
            </Popconfirm>
          ) : (
            <Popconfirm title={t('queue.deleteConfirm')} onConfirm={() => del(j.id)}>
              <Button size="small" type="text" danger icon={<DeleteOutlined />} title={t('common.delete')} />
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ]

  const stat = (label: string, value: number) => (
    <Card size="small" style={{ flex: 1, minWidth: 120 }}>
      <div style={{ fontSize: 24, fontWeight: 500 }}>{value}</div>
      <Typography.Text type="secondary" style={{ fontSize: 13 }}>
        {label}
      </Typography.Text>
    </Card>
  )

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Space wrap style={{ width: '100%' }}>
        {stat(t('queue.running'), summary?.running ?? 0)}
        {stat(t('queue.waiting'), summary?.waiting ?? 0)}
        {stat(t('queue.scheduled'), summary?.scheduled ?? 0)}
        {stat(t('queue.doneToday'), doneToday)}
        {stat(t('queue.budget'), summary?.budget ?? 0)}
      </Space>

      <Card
        title={t('queue.title')}
        extra={
          <Space wrap>
            {summary?.my_priority != null && (
              <span style={{ fontSize: 12 }}>
                <Typography.Text type="secondary">{t('queue.myPriority')}</Typography.Text> {priorityTag(t, String(summary.my_priority))}
              </span>
            )}
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('queue.autoRefresh')}
            </Typography.Text>
            <Switch size="small" checked={auto} onChange={setAuto} />
          </Space>
        }
      >
        <Space wrap style={{ marginBottom: 12 }}>
          <Input.Search allowClear placeholder={t('queue.search')} value={search} onChange={(e) => setSearch(e.target.value)} style={{ width: 200 }} />
          <Select
            allowClear
            placeholder={t('batch.col.status')}
            style={{ width: 130 }}
            value={fStatus || undefined}
            onChange={(v) => setFStatus(v || '')}
            options={[
              { value: 'scheduled', label: t('queue.scheduled') },
              { value: 'queued', label: t('batch.status.queued') },
              { value: 'running', label: t('batch.status.running') },
              { value: 'finished', label: t('batch.status.finished') },
              { value: 'cancelled', label: t('batch.status.cancelled') },
            ]}
          />
          <Select allowClear placeholder={t('batch.col.createdBy')} style={{ width: 140 }} value={fUser || undefined} onChange={(v) => setFUser(v || '')} options={submitters.map((u) => ({ value: u, label: u }))} />
          <Select allowClear placeholder={t('run.workflow')} style={{ width: 200 }} value={fTarget} onChange={setFTarget} options={targets.map((tg) => ({ value: tg.id, label: tg.name }))} />
        </Space>
        {rows.length === 0 ? (
          <Empty description={t('queue.empty')} />
        ) : (
          <Table rowKey="id" size="small" dataSource={rows} columns={cols} pagination={{ pageSize: 15 }} scroll={{ x: 900 }} />
        )}
      </Card>

      <DetailDrawer jobId={detailId} onClose={() => setDetailId(null)} />

      <Modal
        title={t('queue.reschedule')}
        open={reschedId != null}
        onOk={saveReschedule}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        onCancel={() => setReschedId(null)}
        destroyOnClose
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('queue.rescheduleHint')}</Typography.Text>
          <DatePicker showTime value={reschedAt} onChange={setReschedAt} format="YYYY-MM-DD HH:mm:ss" style={{ width: '100%' }} placeholder={t('run.pickTime')} />
        </Space>
      </Modal>
    </Space>
  )
}
