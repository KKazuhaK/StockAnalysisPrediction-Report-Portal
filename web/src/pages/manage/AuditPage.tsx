import { useCallback, useEffect, useState } from 'react'
import { Alert, Card, DatePicker, Input, Select, Space, Table, Tag, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { SearchOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, errText } from '../../api/client'
import type { AuditEntry, AuditResp } from '../../api/types'

// The audit log: who read what, and who changed who can read it.
//
// Two audiences share this table. A client asks the first question and it is the only one the
// portal can answer with evidence rather than assurance; an operator asks the second when something
// is visible that should not be.
//
// Reads are by far the bulk, so the default view leads with everything and lets you narrow — an
// operator arriving here usually knows either the person or the object they are asking about.

// Colour carries the KIND of event, so a wall of rows reads as a shape before it reads as text.
// Anything the portal does not know about renders neutral rather than being hidden.
const ACTION_COLOR: Record<string, string> = {
  'report.read': 'blue',
  'grant.change': 'red',
  'user.change': 'orange',
  'group.change': 'orange',
  'policy.change': 'purple',
}

export default function AuditPage() {
  const { t } = useTranslation()
  const [data, setData] = useState<AuditResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [action, setAction] = useState<string>()
  const [actor, setActor] = useState('')
  const [q, setQ] = useState('')
  const [since, setSince] = useState<string>()
  const [page, setPage] = useState(1)

  const pageSize = 50

  const load = useCallback(() => {
    setLoading(true)
    const qs = new URLSearchParams({ limit: String(pageSize), offset: String((page - 1) * pageSize) })
    if (action) qs.set('action', action)
    if (actor.trim()) qs.set('actor', actor.trim())
    if (q.trim()) qs.set('q', q.trim())
    if (since) qs.set('since', since)
    api
      .get<AuditResp>(`/api/admin/audit?${qs}`)
      .then((r) => {
        setData(r)
        setErr('')
      })
      .catch((e) => setErr(errText(e, t)))
      .finally(() => setLoading(false))
  }, [action, actor, q, since, page, t])
  useEffect(load, [load])

  const ouNames = data?.ou_names ?? {}

  const columns: ColumnsType<AuditEntry> = [
    { title: t('audit.at'), dataIndex: 'at', width: 165 },
    {
      title: t('audit.actor'),
      width: 210,
      render: (_, r) => (
        <Space direction="vertical" size={0}>
          {/* A machine caller has no username. Saying so beats an empty cell, which reads as a bug. */}
          <Typography.Text>{r.actor || t('audit.machine')}</Typography.Text>
          {r.actor_ou > 0 && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {/* The OU they were in AT THE TIME — not where they are now. */}
              {ouNames[String(r.actor_ou)] ?? `OU ${r.actor_ou}`}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t('audit.action'),
      width: 150,
      render: (_, r) => <Tag color={ACTION_COLOR[r.action]}>{t(`audit.a.${r.action}`, r.action)}</Tag>,
    },
    {
      title: t('audit.target'),
      width: 200,
      render: (_, r) => (
        <Typography.Text type="secondary">
          {r.target_type} {r.target_id}
        </Typography.Text>
      ),
    },
    {
      title: t('audit.detail'),
      render: (_, r) => (
        <Typography.Text type="secondary" style={{ fontSize: 12, wordBreak: 'break-all' }}>
          {r.detail}
        </Typography.Text>
      ),
    },
  ]

  return (
    <Card
      title={t('audit.title')}
      extra={
        <Space wrap>
          <Select
            allowClear
            style={{ width: 170 }}
            placeholder={t('audit.action')}
            value={action}
            onChange={(v) => {
              setAction(v)
              setPage(1)
            }}
            // Built from the values present, so rows written by an older build still filter.
            options={(data?.actions ?? []).map((a) => ({ value: a, label: t(`audit.a.${a}`, a) }))}
          />
          <Input
            allowClear
            style={{ width: 170 }}
            placeholder={t('audit.actor')}
            value={actor}
            onChange={(e) => {
              setActor(e.target.value)
              setPage(1)
            }}
          />
          <DatePicker
            placeholder={t('audit.since')}
            value={since ? dayjs(since) : null}
            onChange={(d) => {
              setSince(d ? d.format('YYYY-MM-DD') : undefined)
              setPage(1)
            }}
          />
          <Input
            allowClear
            prefix={<SearchOutlined />}
            style={{ width: 220 }}
            placeholder={t('audit.search')}
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              setPage(1)
            }}
          />
        </Space>
      }
      styles={{ body: { paddingTop: 12 } }}
    >
      {err && <Alert type="error" showIcon message={err} style={{ marginBottom: 12 }} />}
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t('audit.intro')}
      </Typography.Paragraph>
      <Table<AuditEntry>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={data?.items ?? []}
        columns={columns}
        pagination={{
          current: page,
          pageSize,
          total: data?.total ?? 0,
          showSizeChanger: false,
          onChange: setPage,
        }}
      />
    </Card>
  )
}
