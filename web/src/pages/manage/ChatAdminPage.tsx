import { useEffect, useMemo, useRef, useState } from 'react'
import { App, Button, Card, Drawer, Empty, Grid, Input, InputNumber, Popconfirm, Space, Spin, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { EyeOutlined, ReloadOutlined, StopOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { formatReportTime } from '../../lib/datetime'
import Markdown from '../../components/Markdown'
import type { ChatTurn } from '../../api/types'

// Assistant admin (docs/adr/0012-interactive-chat.md). Two independent controls:
//  1. A concurrency ceiling on in-flight chat turns — chat is interactive, so it does NOT
//     go through the batch run queue (that queue defers slow report runs; a chat turn can't
//     wait). This is a simple load-shedding cap, separate from the run budget. 0 = unlimited.
//  2. A live view of the turns in progress right now (who / which assistant / since when).
type ChatLiveTurn = {
  id: number
  user: string
  target_id: number
  target: string
  conv_id: number
  title: string
  started_at: string
}
type ChatLive = { turns: ChatLiveTurn[]; max_concurrent: number; stream: boolean; reconcile_seconds: number }

// One row in the admin conversation-oversight list; messages are fetched on demand (Dify holds them).
type AdminConv = { id: number; created_by: string; target: string; title: string; updated_at: string; started: boolean }

// elapsed renders a short "12s" / "3m 4s" since an ISO instant.
function elapsed(startedAt: string): string {
  const secs = Math.max(0, Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000))
  if (secs < 60) return `${secs}s`
  return `${Math.floor(secs / 60)}m ${secs % 60}s`
}

export default function ChatAdminPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const [limit, setLimit] = useState(0)
  const [stream, setStream] = useState(true)
  const [reconcileSeconds, setReconcileSeconds] = useState(300)
  const [turns, setTurns] = useState<ChatLiveTurn[]>([])
  const [auto, setAuto] = useState(true)
  const seeded = useRef(false)
  const fullWidth = !Grid.useBreakpoint().md
  const [convs, setConvs] = useState<AdminConv[]>([])
  const [convSearch, setConvSearch] = useState('')
  const [viewConv, setViewConv] = useState<AdminConv | null>(null)
  const [viewTurns, setViewTurns] = useState<ChatTurn[]>([])
  const [viewLoading, setViewLoading] = useState(false)

  const load = () =>
    api
      .get<ChatLive>('/api/admin/chat/live')
      .then((r) => {
        setTurns(r.turns || [])
        // Seed the ceiling input once, so a running poll never clobbers an in-progress edit.
        if (!seeded.current) {
          setLimit(r.max_concurrent ?? 0)
          setStream(r.stream !== false)
          setReconcileSeconds(r.reconcile_seconds ?? 300)
          seeded.current = true
        }
      })
      .catch(() => {})
  const loadConvs = () =>
    api.get<{ conversations: AdminConv[] }>('/api/admin/chat/conversations').then((r) => setConvs(r.conversations || [])).catch(() => {})
  useEffect(() => {
    load()
    loadConvs()
  }, [])
  useEffect(() => {
    if (!auto) return
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
  }, [auto])

  const save = async () => {
    await api.post('/api/admin/chat/config', { max_concurrent: limit, stream, reconcile_seconds: reconcileSeconds })
    message.success(t('common.saved'))
  }

  // Open one user's conversation read-only: pull its messages from Dify (keyed by the owner, so the
  // admin sees exactly what that user saw). A conversation with no first turn yet has nothing to show.
  const openConv = (c: AdminConv) => {
    setViewConv(c)
    setViewTurns([])
    if (!c.started) return
    setViewLoading(true)
    api
      .get<{ turns: ChatTurn[] }>(`/api/admin/chat/conversations/${c.id}/messages`)
      .then((r) => setViewTurns(r.turns || []))
      .catch((e) => message.error((e as Error).message || 'failed'))
      .finally(() => setViewLoading(false))
  }
  const filteredConvs = useMemo(() => {
    const q = convSearch.trim().toLowerCase()
    return q ? convs.filter((c) => `${c.created_by} ${c.title} ${c.target}`.toLowerCase().includes(q)) : convs
  }, [convs, convSearch])

  // Stop any in-flight turn from the live view: cancels the Dify stream + best-effort stops the
  // run server-side (runs as the turn's own end-user, not the admin).
  const stop = async (id: number) => {
    try {
      await api.post(`/api/admin/chat/stop/${id}`)
      message.success(t('chatAdmin.stopped'))
      load()
    } catch (e) {
      message.error((e as Error).message || 'failed')
    }
  }

  const columns = [
    { title: t('chatAdmin.colUser'), dataIndex: 'user', key: 'user' },
    { title: t('chatAdmin.colTarget'), dataIndex: 'target', key: 'target' },
    {
      title: t('chatAdmin.colConversation'),
      dataIndex: 'title',
      key: 'title',
      render: (title: string) => title || <Typography.Text type="secondary">{t('chat.untitled')}</Typography.Text>,
    },
    {
      title: t('chatAdmin.colStarted'),
      dataIndex: 'started_at',
      key: 'started_at',
      render: (v: string) => (
        <Tooltip title={formatReportTime(v, true)}>
          <span>{elapsed(v)}</span>
        </Tooltip>
      ),
    },
    {
      title: '',
      key: 'actions',
      width: 48,
      render: (_: unknown, r: ChatLiveTurn) => (
        <Popconfirm title={t('chatAdmin.stopConfirm')} onConfirm={() => stop(r.id)} okButtonProps={{ danger: true }}>
          <Button size="small" type="text" danger icon={<StopOutlined />} title={t('chatAdmin.stop')} />
        </Popconfirm>
      ),
    },
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card title={t('chatAdmin.limitCard')}>
        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Space wrap>
            <span style={{ display: 'inline-block', minWidth: 110 }}>{t('chatAdmin.limit')}</span>
            <InputNumber min={0} max={100} value={limit} onChange={(v) => setLimit(v ?? 0)} />
            <Typography.Text type="secondary">{t('chatAdmin.limitHint')}</Typography.Text>
          </Space>
          <Space wrap>
            <span style={{ display: 'inline-block', minWidth: 110 }}>{t('chatAdmin.stream')}</span>
            <Switch checked={stream} onChange={setStream} />
            <Typography.Text type="secondary">{t('chatAdmin.streamHint')}</Typography.Text>
          </Space>
          <Space wrap>
            <span style={{ display: 'inline-block', minWidth: 110 }}>{t('chatAdmin.reconcile')}</span>
            <InputNumber min={0} max={600} value={reconcileSeconds} onChange={(v) => setReconcileSeconds(v ?? 20)} addonAfter={t('chatAdmin.seconds')} />
            <Typography.Text type="secondary">{t('chatAdmin.reconcileHint')}</Typography.Text>
          </Space>
          <Button type="primary" onClick={save}>
            {t('common.save')}
          </Button>
        </Space>
      </Card>

      <Card
        title={
          <Space>
            {t('chatAdmin.liveCard')}
            <Tag color={turns.length ? 'processing' : 'default'}>{t('chatAdmin.liveCount', { n: turns.length })}</Tag>
          </Space>
        }
        extra={
          <Space>
            <Switch size="small" checked={auto} onChange={setAuto} />
            <Typography.Text type="secondary">{t('queue.autoRefresh')}</Typography.Text>
            <Button size="small" icon={<ReloadOutlined />} onClick={load}>
              {t('nav.refresh')}
            </Button>
          </Space>
        }
      >
        <Table
          size="small"
          rowKey="id"
          columns={columns}
          dataSource={turns}
          pagination={false}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('chatAdmin.none')} /> }}
        />
      </Card>

      <Card
        title={t('chatAdmin.convsCard')}
        extra={
          <Space>
            <Input.Search allowClear placeholder={t('chatAdmin.searchConv')} value={convSearch} onChange={(e) => setConvSearch(e.target.value)} style={{ width: 200 }} />
            <Button size="small" icon={<ReloadOutlined />} onClick={loadConvs}>
              {t('nav.refresh')}
            </Button>
          </Space>
        }
      >
        <Typography.Text type="secondary" style={{ display: 'block', marginBottom: 12 }}>
          {t('chatAdmin.convsHint')}
        </Typography.Text>
        <Table
          size="small"
          rowKey="id"
          dataSource={filteredConvs}
          pagination={{ pageSize: 15, size: 'small' }}
          scroll={{ x: 560 }}
          locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('chatAdmin.convEmpty')} /> }}
          columns={[
            { title: t('chatAdmin.colUser'), dataIndex: 'created_by', width: 140 },
            { title: t('chatAdmin.colTarget'), dataIndex: 'target', width: 150 },
            {
              title: t('chatAdmin.colConversation'),
              dataIndex: 'title',
              render: (title: string, c: AdminConv) =>
                title || <Typography.Text type="secondary">{c.started ? t('chat.untitled') : t('chatAdmin.notStarted')}</Typography.Text>,
            },
            {
              title: t('chatAdmin.colUpdated'),
              dataIndex: 'updated_at',
              width: 168,
              render: (v: string) => <span style={{ fontSize: 12 }}>{formatReportTime(v, true)}</span>,
            },
            {
              title: '',
              key: 'view',
              width: 48,
              render: (_: unknown, c: AdminConv) => (
                <Button size="small" type="text" icon={<EyeOutlined />} onClick={() => openConv(c)} title={t('chatAdmin.view')} />
              ),
            },
          ]}
        />
      </Card>

      <Drawer
        open={viewConv != null}
        onClose={() => setViewConv(null)}
        width={fullWidth ? '100%' : 640}
        destroyOnClose
        title={
          viewConv ? (
            <Space size={8} wrap>
              <Tag>{viewConv.created_by}</Tag>
              <Typography.Text>{viewConv.title || t('chat.untitled')}</Typography.Text>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {viewConv.target}
              </Typography.Text>
            </Space>
          ) : (
            ''
          )
        }
      >
        {viewLoading ? (
          <div style={{ display: 'grid', placeItems: 'center', minHeight: '40vh' }}>
            <Spin />
          </div>
        ) : viewConv && !viewConv.started ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('chatAdmin.notStarted')} />
        ) : viewTurns.length === 0 ? (
          <Empty description={t('chatAdmin.convEmpty')} />
        ) : (
          <Space direction="vertical" size={16} style={{ width: '100%' }}>
            {viewTurns.map((turn, i) => (
              <div key={i}>
                {turn.query && (
                  <div
                    style={{
                      marginBottom: 8,
                      padding: '8px 12px',
                      borderRadius: 8,
                      whiteSpace: 'pre-wrap',
                      background: 'var(--ant-color-fill-secondary)',
                    }}
                  >
                    {turn.query}
                  </div>
                )}
                {turn.answer && <Markdown md={turn.answer} />}
              </div>
            ))}
          </Space>
        )}
      </Drawer>
    </Space>
  )
}
