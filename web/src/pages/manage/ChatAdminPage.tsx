import { useEffect, useRef, useState } from 'react'
import { App, Button, Card, Empty, InputNumber, Space, Switch, Table, Tag, Tooltip, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../../api/client'
import { formatReportTime } from '../../lib/datetime'

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
type ChatLive = { turns: ChatLiveTurn[]; max_concurrent: number }

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
  const [turns, setTurns] = useState<ChatLiveTurn[]>([])
  const [auto, setAuto] = useState(true)
  const seeded = useRef(false)

  const load = () =>
    api
      .get<ChatLive>('/api/admin/chat/live')
      .then((r) => {
        setTurns(r.turns || [])
        // Seed the ceiling input once, so a running poll never clobbers an in-progress edit.
        if (!seeded.current) {
          setLimit(r.max_concurrent ?? 0)
          seeded.current = true
        }
      })
      .catch(() => {})
  useEffect(() => {
    load()
  }, [])
  useEffect(() => {
    if (!auto) return
    const id = setInterval(load, 3000)
    return () => clearInterval(id)
  }, [auto])

  const save = async () => {
    await api.post('/api/admin/chat/config', { max_concurrent: limit })
    message.success(t('common.saved'))
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
  ]

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Card title={t('chatAdmin.limitCard')}>
        <Space wrap>
          <span style={{ display: 'inline-block', minWidth: 96 }}>{t('chatAdmin.limit')}</span>
          <InputNumber min={0} max={100} value={limit} onChange={(v) => setLimit(v ?? 0)} />
          <Button type="primary" onClick={save}>
            {t('common.save')}
          </Button>
          <Typography.Text type="secondary">{t('chatAdmin.limitHint')}</Typography.Text>
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
    </Space>
  )
}
