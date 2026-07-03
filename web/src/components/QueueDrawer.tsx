import { useEffect, useMemo, useState } from 'react'
import { App, Button, Drawer, Empty, Popconfirm, Progress, Space, Tag, Typography } from 'antd'
import { ArrowRightOutlined, ClockCircleOutlined, StopOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { BatchJob, BatchQueueSummary, BatchTarget } from '../api/types'
import { fmtInputs, isTerminal, priorityTag, statusTag } from '../lib/batchUi'

// The header 队列 drawer (docs/adr/0007): a live glance at running / waiting /
// scheduled runs. Polls while open; deeper management lives on the /queue page.
export default function QueueDrawer({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const navigate = useNavigate()
  const [summary, setSummary] = useState<BatchQueueSummary | null>(null)
  const [jobs, setJobs] = useState<BatchJob[]>([])
  const [targets, setTargets] = useState<BatchTarget[]>([])

  const load = () => {
    api.get<BatchQueueSummary>('/api/admin/batch/queue').then(setSummary).catch(() => {})
    api.get<{ jobs: BatchJob[] }>('/api/admin/batch/jobs').then((r) => setJobs(r.jobs || [])).catch(() => {})
  }

  useEffect(() => {
    if (!open) return
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || [])).catch(() => {})
    load()
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
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

  const stat = (label: string, value: number) => (
    <div style={{ flex: 1, textAlign: 'center' }}>
      <div style={{ fontSize: 22, fontWeight: 500 }}>{value}</div>
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
          {stat(t('queue.running'), summary?.running ?? 0)}
          {stat(t('queue.waiting'), summary?.waiting ?? 0)}
          {stat(t('queue.scheduled'), summary?.scheduled ?? 0)}
          {stat(t('queue.budget'), summary?.budget ?? 0)}
        </Space>

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
                      {fmtInputs(j.inputs) && (
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {fmtInputs(j.inputs)}
                        </Typography.Text>
                      )}
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
      </Space>
    </Drawer>
  )
}
