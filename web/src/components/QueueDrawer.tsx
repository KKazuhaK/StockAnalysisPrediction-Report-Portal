import { useEffect, useMemo, useRef, useState } from 'react'
import { App, Button, Drawer, Empty, Popconfirm, Progress, Space, Tag, Typography } from 'antd'
import { ArrowRightOutlined, ClockCircleOutlined, StopOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { api, errText } from '../api/client'
import type { BatchJob, BatchQueueSummary, BatchTarget } from '../api/types'
import { InputsPreview, isTerminal, statusTag } from '../lib/batchUi'
import { UNCHANGED, forgetTags, getIfChanged } from '../lib/conditionalGet'
import { watchQueue } from '../lib/queueWatch'
import { startVisiblePoll } from '../lib/visiblePoll'
import LoadGate from './LoadGate'

// The header 队列 drawer (docs/adr/0007): a live glance at running / waiting /
// scheduled runs. Polls while open; deeper management lives on the /queue page.
export default function QueueDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const navigate = useNavigate()
  const [summary, setSummary] = useState<BatchQueueSummary | null>(null)
  const [jobs, setJobs] = useState<BatchJob[]>([])
  const [targets, setTargets] = useState<BatchTarget[]>([])
  // Same rule as the full queue page: `[]` and a wall of zeros are where the state starts, not
  // an answer. Nothing is said about the queue until the jobs call has landed at least once.
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  // What this drawer is actually holding. The tag store is module-global, and the header badge
  // polls the same summary URL — so on the first open a 304 would answer "unchanged" about a
  // summary the drawer has never seen, leaving all four tiles on a dash for good.
  const heldSummary = useRef(false)
  const heldJobs = useRef(false)

  const load = async () => {
    if (!heldSummary.current) forgetTags('/api/admin/batch/queue')
    if (!heldJobs.current) forgetTags('/api/admin/batch/jobs')
    const summaryRequest = getIfChanged<BatchQueueSummary>('/api/admin/batch/queue')
      .then((r) => {
        if (r === UNCHANGED) return
        setSummary(r)
        heldSummary.current = true
      })
      .catch(() => {})
    const jobsRequest = getIfChanged<{ jobs: BatchJob[] }>('/api/admin/batch/jobs').then((r) => {
      if (r === UNCHANGED) return
      setJobs(r.jobs || [])
      heldJobs.current = true
    })
    try {
      await jobsRequest
      if (heldJobs.current) {
        setLoaded(true)
        setLoadErr('')
      }
    } catch (e) {
      // Read only while !loaded: a poll that fails once the list is on screen changes nothing,
      // since those runs are still the last thing the server said about the queue.
      setLoadErr(errText(e, t))
    }
    await summaryRequest
  }

  useEffect(() => {
    if (!open) return
    // Reopening asks again, so the drawer must not present the previous open's answer as this
    // one's — but it keeps the rows it has so a reopen is not a flash of spinner.
    setLoadErr('')
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || [])).catch(() => {})
    const unwatch = watchQueue() // the badge stands down while the drawer shows the same thing
    const stop = startVisiblePoll(load, 3000)
    return () => {
      stop()
      unwatch()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open])

  const targetName = (id: number) => targets.find((tg) => tg.id === id)?.name || `#${id}`
  // Active runs first: running, then waiting, then scheduled. Terminal ones live on the full page.
  const active = useMemo(() => jobs.filter((j) => !isTerminal(j.status)), [jobs])

  const cancel = async (id: number) => {
    await api.post(`/api/admin/batch/jobs/${id}/cancel`)
    message.success(t('batch.msg.cancelRequested'))
    load()
  }

  // null = not answered yet; a dash rather than a 0, which would read as a settled idle queue.
  const stat = (label: string, value: number | null) => (
    <div style={{ flex: 1, textAlign: 'center' }}>
      <div style={{ fontSize: 22, fontWeight: 500 }}>{value ?? <Typography.Text type="secondary">—</Typography.Text>}</div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {label}
      </Typography.Text>
    </div>
  )

  return (
    <Drawer
      title={t('queue.title')}
      width={420}
      open={open}
      onClose={onClose}
      extra={
        <Button type="link" icon={<ArrowRightOutlined />} onClick={() => { onClose(); navigate('/queue') }}>
          {t('queue.openFull')}
        </Button>
      }
    >
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Space style={{ width: '100%', justifyContent: 'space-around' }} split={<span style={{ color: 'var(--rp-border)' }}>|</span>}>
          {stat(t('queue.running'), summary?.running ?? null)}
          {stat(t('queue.waiting'), summary?.waiting ?? null)}
          {stat(t('queue.scheduled'), summary?.scheduled ?? null)}
          {stat(t('queue.budget'), summary?.budget ?? null)}
        </Space>

        <LoadGate
          loading={!loaded && !loadErr}
          error={loaded ? undefined : loadErr}
          onRetry={load}
          minHeight={200}
          title={t('common.loadFailedContent')}
        >
        {active.length === 0 ? (
          <Empty description={t('queue.empty')} />
        ) : (
          <Space direction="vertical" size={0} style={{ width: '100%' }}>
            {active.map((j) => {
              const done = j.succeeded + j.partial + j.failed
              const pct = j.total ? Math.round((done / j.total) * 100) : 0
              return (
                <div key={j.id} style={{ padding: '10px 0', borderTop: '0.5px solid var(--rp-border, rgba(128,128,128,0.2))' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: 8 }}>
                    <div style={{ minWidth: 0 }}>
                      <div style={{ fontSize: 14 }}>
                        {targetName(j.target_id)}
                        {j.priority === 'urgent' && (
                          <ThunderboltOutlined style={{ color: '#cf1322', marginLeft: 6 }} title={t('batch.priority.urgent')} />
                        )}
                      </div>
                      <InputsPreview inputs={j.inputs} />
                    </div>
                    <Space size={4}>
                      {statusTag(t, j.status)}
                      {!isTerminal(j.status) && (
                        <Popconfirm title={t('queue.cancelConfirm')} onConfirm={() => cancel(j.id)}>
                          <Button size="small" danger type="text" icon={<StopOutlined />} />
                        </Popconfirm>
                      )}
                    </Space>
                  </div>
                  <div style={{ marginTop: 6 }}>
                    {j.scheduled ? (
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        <ClockCircleOutlined /> {t('queue.scheduledAt', { at: j.run_at })}
                      </Typography.Text>
                    ) : j.status === 'queued' ? (
                      <Tag color="default">{j.ahead ? t('batch.aheadN', { n: j.ahead }) : t('batch.aheadNext')}</Tag>
                    ) : (
                      <Progress percent={pct} size="small" status={j.failed ? 'exception' : 'active'} />
                    )}
                  </div>
                </div>
              )
            })}
          </Space>
        )}
        </LoadGate>
      </Space>
    </Drawer>
  )
}
