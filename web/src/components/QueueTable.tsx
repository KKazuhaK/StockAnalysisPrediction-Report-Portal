import { useEffect, useMemo, useState, type Key } from 'react'
import {
  App,
  Button,
  Card,
  DatePicker,
  Descriptions,
  Drawer,
  Empty,
  Grid,
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
  InfoCircleOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  StopOutlined,
  SyncOutlined,
} from '@ant-design/icons'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { Dayjs } from 'dayjs'
import { api } from '../api/client'
import { useAuth } from '../auth'
import type { BatchItem, BatchJob, BatchJobDetail, BatchQueueSummary, BatchTarget } from '../api/types'
import { BASE_MAX, fmtInputs, isTerminal, isUrgent, priorityNum, priorityTag, statusTag } from '../lib/batchUi'

const todayStr = () => new Date().toISOString().slice(0, 10)

// itemActive reports whether a run (row) can still be cancelled — it hasn't reached a
// terminal state (succeeded / partial / failed / cancelled).
const itemActive = (s: string) => s === 'queued' || s === 'running'

// Detail drawer: a run's rows, inputs, errors + a link to the stock's reports. Rows can be
// cancelled individually (per-row ⊘) or several at once (checkbox multi-select), by the
// job's owner or an admin.
function DetailDrawer({ jobId, admin, user, onClose }: { jobId: number | null; admin: boolean; user: string | null; onClose: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const fullWidth = !Grid.useBreakpoint().md // phone: the 680px drawer would overflow the screen
  const [detail, setDetail] = useState<BatchJobDetail | null>(null)
  const [selected, setSelected] = useState<Key[]>([])
  const [detailItem, setDetailItem] = useState<BatchItem | null>(null)
  const open = jobId != null
  const load = () => (jobId == null ? Promise.resolve() : api.get<BatchJobDetail>(`/api/admin/batch/jobs/${jobId}`).then(setDetail).catch(() => {}))
  useEffect(() => {
    if (jobId == null) return
    setDetail(null)
    setSelected([])
    load()
    const id = setInterval(load, 2500)
    return () => clearInterval(id)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId])

  const canCancel = detail != null && (admin || detail.job.created_by === user)
  const cancelRows = async (ids: Key[]) => {
    if (jobId == null || ids.length === 0) return
    try {
      await api.post(`/api/admin/batch/jobs/${jobId}/items/cancel`, { item_ids: ids.map(Number) })
      message.success(t('batch.msg.rowsCancelled', { n: ids.length }))
      setSelected([])
      await load()
    } catch (e) {
      message.error((e as Error).message || 'failed')
    }
  }

  // A row that has a Dify handle can be reconciled: fetch its true outcome from Dify without
  // re-running it. Rescues a run wrongly marked failed by a short timeout / dropped stream.
  const hasHandle = (it: BatchItem) => !!(it.run_id || it.conversation_id)
  const canReconcile = (it: BatchItem) => admin && hasHandle(it) && !itemActive(it.status) && it.status !== 'succeeded'
  const reconcile = async (it: BatchItem) => {
    try {
      const r = await api.post<{ status: string; note?: string }>(`/api/admin/batch/items/${it.id}/reconcile`)
      message.success(r.note ? t('queue.reconcileStillRunning') : t('queue.reconcileDone', { status: t(`batch.status.${r.status}`) }))
      await load()
    } catch (e) {
      message.error((e as Error).message || 'failed')
    }
  }
  const idCell = (v: string) =>
    v ? (
      <Typography.Text copyable style={{ fontSize: 12 }}>
        {v}
      </Typography.Text>
    ) : (
      <Typography.Text type="secondary">—</Typography.Text>
    )

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
    {
      title: '',
      width: 96,
      render: (_: unknown, it) => (
        <Space size={0}>
          <Button
            size="small"
            type="text"
            icon={<InfoCircleOutlined />}
            title={t('queue.itemDetails')}
            onClick={() => setDetailItem(it)}
          />
          {canReconcile(it) ? (
            <Popconfirm title={t('queue.reconcileConfirm')} onConfirm={() => reconcile(it)}>
              <Button size="small" type="text" icon={<SyncOutlined />} title={t('queue.reconcile')} />
            </Popconfirm>
          ) : null}
          {canCancel && itemActive(it.status) ? (
            <Popconfirm title={t('queue.cancelRowConfirm')} onConfirm={() => cancelRows([it.id])}>
              <Button size="small" type="text" danger icon={<StopOutlined />} title={t('queue.cancelRow')} />
            </Popconfirm>
          ) : null}
        </Space>
      ),
    },
  ]
  const rowSelection = canCancel
    ? {
        selectedRowKeys: selected,
        onChange: setSelected,
        getCheckboxProps: (it: BatchItem) => ({ disabled: !itemActive(it.status) }),
      }
    : undefined
  return (
    <Drawer
      title={detail ? t('batch.jobTitle', { id: detail.job.id }) : t('batch.jobDetail')}
      width={fullWidth ? '100%' : 680}
      open={open}
      onClose={onClose}
      destroyOnClose
      extra={
        selected.length > 0 ? (
          <Popconfirm title={t('queue.cancelRowsConfirm', { n: selected.length })} onConfirm={() => cancelRows(selected)}>
            <Button danger size="small" icon={<StopOutlined />}>
              {t('queue.cancelSelected', { n: selected.length })}
            </Button>
          </Popconfirm>
        ) : null
      }
    >
      {detail && (
        <Table
          rowKey="id"
          size="small"
          rowSelection={rowSelection}
          dataSource={detail.items}
          columns={cols}
          pagination={{ pageSize: 20, size: 'small' }}
          scroll={{ x: 560 }}
        />
      )}
      <Modal open={detailItem != null} title={t('queue.itemDetails')} footer={null} width={560} onCancel={() => setDetailItem(null)}>
        {detailItem && (
          <Descriptions bordered size="small" column={1}>
            <Descriptions.Item label={t('batch.col.row')}>{detailItem.row_index}</Descriptions.Item>
            <Descriptions.Item label={t('batch.col.status')}>{statusTag(t, detailItem.status)}</Descriptions.Item>
            <Descriptions.Item label={t('queue.attempts')}>{detailItem.attempts}</Descriptions.Item>
            <Descriptions.Item label="run_id">{idCell(detailItem.run_id)}</Descriptions.Item>
            <Descriptions.Item label="conversation_id">{idCell(detailItem.conversation_id)}</Descriptions.Item>
            <Descriptions.Item label="task_id">{idCell(detailItem.task_id)}</Descriptions.Item>
            <Descriptions.Item label={t('queue.startedAt')}>{detailItem.started_at || '—'}</Descriptions.Item>
            <Descriptions.Item label={t('queue.finishedAt')}>{detailItem.finished_at || '—'}</Descriptions.Item>
            <Descriptions.Item label={t('batch.col.inputs')}>{fmtInputs(detailItem.inputs)}</Descriptions.Item>
            {detailItem.error ? (
              <Descriptions.Item label={t('batch.col.error')}>
                <Typography.Text type="danger" style={{ fontSize: 12 }}>
                  {detailItem.error}
                </Typography.Text>
              </Descriptions.Item>
            ) : null}
          </Descriptions>
        )}
      </Modal>
    </Drawer>
  )
}

// The full run queue: filters + jobs table + all queue actions + detail drawer +
// reschedule modal. Shared by the Run/queue page (with stats) and the batch console
// (embedded under the run form) so both show the same complete view. Queue mutations
// are gated to match the server: a non-admin sees only "cancel" and only on their own
// runs; admins see cancel/terminate/retry/reschedule/reprioritize/delete on any job.
export default function QueueTable({ showStats = false }: { showStats?: boolean }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { admin, user } = useAuth()
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
  const [selectedJobs, setSelectedJobs] = useState<Key[]>([])

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
  const canCancel = (j: BatchJob) => admin || j.created_by === user // matches the server ownership check

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
  // Cancel several whole jobs at once (multi-select). Each goes through the same per-job
  // cancel endpoint (which the server authorizes per job), so partial permission is fine.
  const cancelJobs = (ids: Key[]) =>
    act(async () => {
      await Promise.allSettled(ids.map((id) => api.post(`/api/admin/batch/jobs/${Number(id)}/cancel`)))
      setSelectedJobs([])
    }, t('batch.msg.cancelRequested'))
  const del = (id: number) => act(() => api.del(`/api/admin/batch/jobs/${id}`), t('queue.deleted'))
  const retry = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/retry`, { statuses: ['failed'] }), t('queue.retried'))
  const runNow = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/schedule`, { run_at: '' }), t('queue.runningNow'))
  const reprioritize = (id: number, p: string) => act(() => api.post(`/api/admin/batch/jobs/${id}/priority`, { priority: p }))
  const saveReschedule = () =>
    act(
      () => api.post(`/api/admin/batch/jobs/${reschedId}/schedule`, { run_at: reschedAt ? reschedAt.format('YYYY-MM-DD HH:mm:ss') : '' }),
      t('queue.rescheduled'),
    ).then(() => {
      setReschedId(null)
      setReschedAt(null)
    })
  const clearFinished = async () => {
    try {
      const r = await api.post<{ n: number }>('/api/admin/batch/jobs/clear-finished')
      message.success(t('queue.clearedN', { n: r.n }))
      load()
    } catch (e) {
      message.error((e as Error).message || 'failed')
    }
  }

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
      // A queued non-urgent job gets an inline base-priority editor (admin only); urgent
      // and non-queued jobs, and non-admins, show a static tag (ADR 0008).
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
        if (j.status === 'queued') return <Tag>{j.ahead ? t('batch.aheadN', { n: j.ahead }) : t('batch.aheadNext')}</Tag>
        const cancelled = j.cancelled || 0
        const done = j.succeeded + j.partial + j.failed + cancelled // cancelled rows are terminal too
        const running = j.status === 'running' || j.status === 'cancelling'
        const realPct = j.total ? Math.round((done / j.total) * 100) : 0
        // A running job with no measurable progress yet (a single-row run, or a batch
        // whose first row is still going) shows an indeterminate "loading" bar — a full
        // animated stripe — instead of an empty 0% one.
        const loading = running && realPct === 0
        // Terminal colour: some-ok+some-fail (partial success) yellow, all-failed red,
        // any success green, all-cancelled/none neutral.
        const anyFail = j.failed > 0
        const anyOk = j.succeeded > 0 || j.partial > 0
        const status = running ? 'active' : anyFail && !anyOk ? 'exception' : !anyFail && anyOk ? 'success' : undefined
        const strokeColor = !running && anyFail && anyOk ? '#faad14' : undefined // partial → yellow
        return (
          <div style={{ maxWidth: 200 }}>
            <Progress
              percent={loading ? 100 : realPct}
              size="small"
              status={status}
              strokeColor={strokeColor}
              // Indeterminate "loading" bar: animate a full bar but hide the "100%" —
              // the run is at 0/1, not done, so the number would be a lie.
              showInfo={!loading}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: -2 }}>
              {t('batch.progressText', { done, total: j.total, ok: j.succeeded, fail: j.failed, partial: j.partial })}
              {cancelled > 0 ? ` · ${t('batch.cancelledN', { n: cancelled })}` : ''}
            </Typography.Text>
          </div>
        )
      },
    },
    {
      title: t('batch.col.actions'),
      width: 150,
      render: (_: unknown, j) => (
        <Space size={2}>
          <Button size="small" type="text" icon={<EyeOutlined />} onClick={() => setDetailId(j.id)} title={t('batch.view')} />
          {admin && j.scheduled && <Button size="small" type="text" icon={<PlayCircleOutlined />} onClick={() => runNow(j.id)} title={t('queue.runNow')} />}
          {admin && j.status === 'queued' && (
            <Button size="small" type="text" icon={<ClockCircleOutlined />} onClick={() => { setReschedId(j.id); setReschedAt(null) }} title={t('queue.reschedule')} />
          )}
          {admin && isTerminal(j.status) && j.failed > 0 && (
            <Button size="small" type="text" icon={<ReloadOutlined />} onClick={() => retry(j.id)} title={t('batch.retryFailed', { n: j.failed })} />
          )}
          {!isTerminal(j.status)
            ? canCancel(j) && (
                <Popconfirm title={t('queue.cancelConfirm')} onConfirm={() => cancel(j.id)}>
                  <Button size="small" type="text" danger icon={<StopOutlined />} title={t('common.cancel')} />
                </Popconfirm>
              )
            : admin && (
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
      {showStats && (
        <Space wrap style={{ width: '100%' }}>
          {stat(t('queue.running'), summary?.running ?? 0)}
          {stat(t('queue.waiting'), summary?.waiting ?? 0)}
          {stat(t('queue.scheduled'), summary?.scheduled ?? 0)}
          {stat(t('queue.doneToday'), doneToday)}
          {stat(t('queue.budget'), summary?.budget ?? 0)}
        </Space>
      )}

      <Card
        title={t('queue.title')}
        extra={
          <Space wrap>
            {summary?.budget != null && (
              <span style={{ fontSize: 12 }}>
                <Typography.Text type="secondary">{t('queue.budget')}</Typography.Text>{' '}
                <Tag>
                  {summary.running_rows ?? summary.running ?? 0} / {summary.budget}
                </Tag>
              </span>
            )}
            {summary?.my_priority != null && (
              <span style={{ fontSize: 12 }}>
                <Typography.Text type="secondary">{t('queue.myPriority')}</Typography.Text> {priorityTag(t, String(summary.my_priority))}
              </span>
            )}
            {selectedJobs.length > 0 && (
              <Popconfirm title={t('queue.cancelJobsConfirm', { n: selectedJobs.length })} onConfirm={() => cancelJobs(selectedJobs)}>
                <Button size="small" danger icon={<StopOutlined />}>
                  {t('queue.cancelSelected', { n: selectedJobs.length })}
                </Button>
              </Popconfirm>
            )}
            {admin && (
              <Popconfirm title={t('queue.clearFinishedConfirm')} onConfirm={clearFinished}>
                <Button size="small" icon={<DeleteOutlined />}>
                  {t('queue.clearFinished')}
                </Button>
              </Popconfirm>
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
          <Table
            rowKey="id"
            size="small"
            rowSelection={{
              selectedRowKeys: selectedJobs,
              onChange: setSelectedJobs,
              // Only in-flight jobs (their owner/admin) can be cancelled; terminal or
              // others' jobs aren't selectable.
              getCheckboxProps: (j) => ({ disabled: isTerminal(j.status) || !canCancel(j) }),
            }}
            dataSource={rows}
            columns={cols}
            pagination={{ pageSize: 15 }}
            // The fixed columns sum to ~900px; give the flexible workflow column real room
            // (and let the table scroll horizontally on phones) instead of crushing it to a
            // one-character-per-line sliver.
            scroll={{ x: 1160 }}
          />
        )}
      </Card>

      <DetailDrawer jobId={detailId} admin={admin} user={user} onClose={() => setDetailId(null)} />

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
