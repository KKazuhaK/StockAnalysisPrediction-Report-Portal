import { useEffect, useMemo, useRef, useState, type Key } from 'react'
import {
  Alert,
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
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { Dayjs } from 'dayjs'
import { api, errText } from '../api/client'
import { useAuth } from '../auth'
import type { BatchItem, BatchJob, BatchJobDetail, BatchQueueSummary, BatchTarget } from '../api/types'
import {
  BASE_MAX,
  clampText,
  fmtInputs,
  INPUT_TOTAL_MAX,
  InputsPreview,
  isTerminal,
  isUrgent,
  priorityNum,
  priorityTag,
  statusTag,
  TOOLTIP_STYLES,
} from '../lib/batchUi'
import { UNCHANGED, forgetTags, getIfChanged } from '../lib/conditionalGet'
import { watchQueue } from '../lib/queueWatch'
import { startVisiblePoll } from '../lib/visiblePoll'
import LoadGate from './LoadGate'

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
    return startVisiblePoll(load, 2500)
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
      message.error(errText(e, t))
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
      message.error(errText(e, t))
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
    { title: t('batch.col.inputs'), render: (_: unknown, it) => <InputsPreview inputs={it.inputs} rows={3} secondary={false} /> },
    { title: t('batch.col.status'), dataIndex: 'status', width: 92, render: (s: string) => statusTag(t, s) },
    {
      title: t('batch.col.error'),
      render: (_: unknown, it) =>
        it.error ? (
          // Same bound as the inputs preview: a raw provider error can run to thousands of
          // characters, and an overlay that tall gets clipped by the viewport and flickers.
          // The full text is in this row's detail modal.
          <Typography.Paragraph
            type="danger"
            ellipsis={{ rows: 3, tooltip: { title: clampText(it.error, INPUT_TOTAL_MAX), styles: TOOLTIP_STYLES } }}
            style={{ fontSize: 12, marginBottom: 0 }}
          >
            {it.error}
          </Typography.Paragraph>
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
      destroyOnHidden
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
      {/* A job the queue calls "running" is not necessarily running HERE: after a restart the row
          survives while the goroutine driving it does not, and the portal reconciles it by polling
          rather than by executing. The server has always computed this; nothing rendered it, so the
          one signal that tells an admin whether a stuck job is stuck in THIS process or merely
          being watched was unreachable. Only said when it is worth saying — a running job. */}
      {detail && detail.job.status === 'running' && !detail.running_in_process && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message={t('queue.notInProcess')}
          description={t('queue.notInProcessHint')}
        />
      )}
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
  const mobile = !Grid.useBreakpoint().md
  const [jobs, setJobs] = useState<BatchJob[]>([])
  const [total, setTotal] = useState(0)
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
  // Has the first jobs answer landed? `jobs` starts at [] because that is where a useState
  // starts, not because the queue is empty — and "Queue is empty" is a claim about the server.
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const [targetsLoaded, setTargetsLoaded] = useState(false) // the workflow filter's option list
  // Which answers this component is actually holding. A 304 means "unchanged since the tag YOU
  // sent", and the tag store is module-global and outlives any one mount — so without these,
  // arriving on /queue after the header badge has tagged the summary, or clearing the search box
  // back to a URL tagged before the current rows, is answered "nothing changed" about data this
  // component does not have. Refs, not state: `load` is captured by a poll started in an effect.
  const heldJobsUrl = useRef('')
  const heldSummary = useRef(false)

  const load = async () => {
    // Bounded poll: the server returns every active job + the most recent terminal jobs (not the whole
    // history) plus the true total, so the 3s poll stays cheap on a large finished-job backlog.
    // The search goes with it: since the list carries only a PREVIEW of each job's inputs, filtering
    // here would stop matching anything past the first line of a prompt. The server still sees them
    // whole.
    // Conditional: a queue in which nothing happened answers 304, and keeping the objects we
    // already have is what stops a 3-second poll from re-rendering the table for no reason.
    const jobsUrl = `/api/admin/batch/jobs?limit=300${search.trim() ? `&q=${encodeURIComponent(search.trim())}` : ''}`
    // Drop the tag for anything we are not holding, so this request has to come back as an answer.
    if (heldJobsUrl.current !== jobsUrl) forgetTags(jobsUrl)
    if (!heldSummary.current) forgetTags('/api/admin/batch/queue')
    const jobsRequest = getIfChanged<{ jobs: BatchJob[]; total?: number }>(jobsUrl).then((r) => {
      if (r === UNCHANGED) return
      setJobs(r.jobs || [])
      setTotal(r.total ?? (r.jobs || []).length)
      heldJobsUrl.current = jobsUrl
    })
    const summaryRequest = getIfChanged<BatchQueueSummary>('/api/admin/batch/queue')
      .then((r) => {
        if (r === UNCHANGED) return
        setSummary(r)
        heldSummary.current = true
      })
      .catch(() => {})
    try {
      await jobsRequest
      // Only once rows for THIS url have actually landed. The guard above means an UNCHANGED can
      // no longer arrive for a url we are not holding, but the gate is what stands between the
      // reader and "Queue is empty", so it does not take that on trust.
      if (heldJobsUrl.current === jobsUrl) {
        setLoaded(true)
        setLoadErr('')
      }
    } catch (e) {
      // Only the first load has anything to report. Once rows are on screen a failed poll is
      // not news — they are still the last thing the server said, and swapping them for an
      // error page would be the worse answer. The render below reads this only while !loaded.
      setLoadErr(errText(e, t))
    }
    await summaryRequest
  }
  useEffect(() => {
    api
      .get<{ targets: BatchTarget[] }>('/api/admin/batch/targets')
      .then((r) => setTargets(r.targets || []))
      .catch(() => {})
      .finally(() => setTargetsLoaded(true))
    // While this view is mounted the header badge stands down: it would be asking, four times
    // slower, for what is on screen in full.
    return watchQueue()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    if (!auto) return
    return startVisiblePoll(load, 3000)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto, search])

  // Typing must not fire a request per keystroke, and with auto-refresh off nothing else would
  // refetch at all, so a paused queue still answers the search box.
  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 300)
    return () => window.clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search])

  const targetName = (id: number) => targets.find((tg) => tg.id === id)?.name || `#${id}`
  const submitters = useMemo(() => [...new Set(jobs.map((j) => j.created_by).filter(Boolean))], [jobs])
  // Prefer the server-side count (exact even though the job list is paginated); fall back to counting
  // the loaded page for an older server that doesn't send done_today — and only once that page is
  // actually loaded, since counting [] would answer "0 today" before anyone had asked the server.
  const doneToday = useMemo(
    () => summary?.done_today ?? (loaded ? jobs.filter((j) => isTerminal(j.status) && (j.finished_at || '').startsWith(todayStr())).length : null),
    [summary, jobs, loaded],
  )
  const canCancel = (j: BatchJob) => admin || j.created_by === user // matches the server ownership check

  // Status / submitter / workflow filter the fetched set — they read fields the list still carries
  // in full. The free-text search does not appear here: it is applied by the server (see load).
  const rows = useMemo(() => {
    return jobs.filter((j) => {
      if (fStatus === 'scheduled' ? !j.scheduled : fStatus && j.status !== fStatus) return false
      if (fUser && j.created_by !== fUser) return false
      if (fTarget && j.target_id !== fTarget) return false
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
      message.error(errText(e, t))
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
      message.error(errText(e, t))
    }
  }

  const cols: ColumnsType<BatchJob> = [
    {
      title: t('queue.colWorkflow'),
      render: (_: unknown, j) => (
        <div>
          <div style={{ fontSize: 13 }}>{targetName(j.target_id)}</div>
          <InputsPreview inputs={j.inputs} />
        </div>
      ),
    },
    { title: t('batch.col.createdBy'), dataIndex: 'created_by', width: 110, responsive: ['md'] },
    { title: t('queue.colSubmitted'), dataIndex: 'created_at', width: 160, responsive: ['lg'], render: (s: string) => <span style={{ fontSize: 12 }}>{s}</span> },
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
      responsive: ['md'],
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

  // null = the summary has not landed yet. A dash, not a 0: five tiles reading zero are a
  // statement about the queue, and rendering it before the server has answered makes the page
  // look like it has finished loading on an idle queue.
  const stat = (label: string, value: number | null) => (
    <Card size="small" styles={{ body: { padding: mobile ? '8px 12px' : undefined } }}>
      <div style={{ fontSize: mobile ? 20 : 24, fontWeight: 500, lineHeight: 1.2 }}>{value ?? <Typography.Text type="secondary">—</Typography.Text>}</div>
      <Typography.Text type="secondary" style={{ fontSize: mobile ? 12 : 13 }}>
        {label}
      </Typography.Text>
    </Card>
  )

  // Budget / my-priority / clear-finished / auto-refresh. In the card header (extra) on
  // desktop; on a phone that corner is too cramped, so they move to a full-width body row.
  const queueControls = (
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
  )

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      {showStats && (
        // A responsive grid (not Space wrap): auto-fit + 1fr keeps every tile the same size
        // and stretched to fill its row, so the stats never leave a lone half-empty card.
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(96px, 1fr))', gap: 10, width: '100%' }}>
          {stat(t('queue.running'), summary?.running ?? null)}
          {stat(t('queue.waiting'), summary?.waiting ?? null)}
          {stat(t('queue.scheduled'), summary?.scheduled ?? null)}
          {stat(t('queue.doneToday'), doneToday)}
          {stat(t('queue.budget'), summary?.budget ?? null)}
        </div>
      )}

      <Card title={t('queue.title')} extra={mobile ? undefined : queueControls}>
        {mobile && <div style={{ marginBottom: 12 }}>{queueControls}</div>}
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
          <Select showSearch optionFilterProp="label" allowClear placeholder={t('batch.col.createdBy')} style={{ width: 140 }} value={fUser || undefined} onChange={(v) => setFUser(v || '')} options={submitters.map((u) => ({ value: u, label: u }))} />
          {/* loading, not an empty dropdown: "no workflows" is not something this filter knows. */}
          <Select showSearch optionFilterProp="label" allowClear loading={!targetsLoaded} placeholder={t('run.workflow')} style={{ width: 200 }} value={fTarget} onChange={setFTarget} options={targets.map((tg) => ({ value: tg.id, label: tg.name }))} />
        </Space>
        {total > jobs.length && (
          <Typography.Text type="secondary" style={{ display: 'block', marginBottom: 8 }}>
            {t('queue.truncatedHint', { shown: jobs.length, total })}
          </Typography.Text>
        )}
        {/* Until the first answer lands, "Queue is empty" is a claim about the server that the
            server has not made — and with auto-refresh off nothing would ever correct it. */}
        <LoadGate
          loading={!loaded && !loadErr}
          error={loaded ? undefined : loadErr}
          onRetry={load}
          minHeight={220}
          title={t('common.loadFailedContent')}
        >
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
            // Desktop shows every column (~1160px, scrolls); on mobile the secondary columns
            // (submitter / time / priority) are hidden, so the essentials — workflow, status,
            // progress, actions — fit a much narrower scroll instead of a 1160px sliver.
            scroll={{ x: mobile ? 640 : 1160 }}
          />
        )}
        </LoadGate>
      </Card>

      <DetailDrawer jobId={detailId} admin={admin} user={user} onClose={() => setDetailId(null)} />

      <Modal
        title={t('queue.reschedule')}
        open={reschedId != null}
        onOk={saveReschedule}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        onCancel={() => setReschedId(null)}
        destroyOnHidden
      >
        <Space direction="vertical" style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('queue.rescheduleHint')}</Typography.Text>
          <DatePicker showTime={{ format: 'HH:mm' }} value={reschedAt} onChange={setReschedAt} format="YYYY-MM-DD HH:mm" popupClassName="rp-picker-popup" style={{ width: '100%' }} placeholder={t('run.pickTime')} />
        </Space>
      </Modal>
    </Space>
  )
}
