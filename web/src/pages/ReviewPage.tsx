import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, App, Button, Card, Input, Select, Space, Table, Tag, Typography, theme } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { SearchOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { api, errText } from '../api/client'
import type { TrackingResp, TrackingRow } from '../api/types'

// The review queue: what the reports ASSUMED, and whether it held.
//
// Every ingest may attach the assumptions a report rests on and when each should be checked
// (`tracking` on POST /api/v1/reports). Without somewhere to revisit them the assumptions are just
// prose; this is the page that turns them into a list you can work through.
//
// It is normal for this to be empty. The data comes from the workflow, so the empty state has to
// teach rather than shrug — an operator who lands here and sees "no data" learns nothing about how
// to get some. The portal already answers this way elsewhere: SSO with no provider, registration
// with no SMTP, compare with no earlier edition.

// The one status the portal itself assigns; everything else in the vocabulary comes from the
// pipeline, so the filter is built from the data rather than from a list invented here.
const PENDING = 'pending'

// The verdicts a human can record. Offered as suggestions, not enforced — the ingest contract lets
// a workflow use its own vocabulary, and the Select accepts anything typed into it.
const VERDICTS = ['confirmed', 'invalidated', 'expired']

export default function ReviewPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { token } = theme.useToken()
  const navigate = useNavigate()

  const [data, setData] = useState<TrackingResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [status, setStatus] = useState<string | undefined>(PENDING)
  const [itype, setIType] = useState<string>()
  const [q, setQ] = useState('')
  const [sort, setSort] = useState<'due' | ''>('due')

  const load = useCallback(() => {
    setLoading(true)
    const qs = new URLSearchParams()
    if (status) qs.set('status', status)
    if (itype) qs.set('itype', itype)
    if (q.trim()) qs.set('q', q.trim())
    if (sort) qs.set('sort', sort)
    api
      .get<TrackingResp>(`/api/tracking?${qs}`)
      .then((r) => {
        setData(r)
        setErr('')
      })
      .catch((e) => setErr(errText(e, t)))
      .finally(() => setLoading(false))
  }, [status, itype, q, sort, t])
  useEffect(load, [load])

  const review = async (row: TrackingRow, verdict: string) => {
    try {
      await api.patch(`/api/tracking/${row.id}`, { status: verdict })
      message.success(t('common.saved'))
      load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const counts = data?.counts ?? {}
  const total = Object.values(counts).reduce((a, b) => a + b, 0)

  const columns: ColumnsType<TrackingRow> = [
    {
      title: t('review.item'),
      render: (_, r) => (
        <Space direction="vertical" size={2}>
          <Typography.Text>{r.content}</Typography.Text>
          <Space size={6} wrap>
            {r.itype && <Tag>{r.itype}</Tag>}
            {/* The report the assumption came from — clicking through is the point: a claim with
                no context behind it cannot be judged. */}
            <a onClick={() => navigate(`/stock/${r.symbol}?date=${r.report_date}`)}>
              {r.symbol} {r.name} · {r.report_date} · {r.report_title}
            </a>
          </Space>
        </Space>
      ),
    },
    {
      title: t('review.reviewPoint'),
      width: 220,
      render: (_, r) =>
        r.due ? (
          // A parsed date is shown as one; the raw text stays visible because it usually says
          // WHAT to check, not only when.
          <Space direction="vertical" size={0}>
            <Typography.Text style={{ color: token.colorWarning }}>{r.due}</Typography.Text>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {r.review_point}
            </Typography.Text>
          </Space>
        ) : (
          <Typography.Text type="secondary">{r.review_point || '—'}</Typography.Text>
        ),
    },
    { title: t('review.status'), width: 110, render: (_, r) => <Tag>{r.status}</Tag> },
    {
      title: '',
      width: 210,
      render: (_, r) =>
        r.status === PENDING ? (
          <Space size={4}>
            {VERDICTS.map((v) => (
              <Button key={v} size="small" onClick={() => review(r, v)}>
                {t(`review.verdict.${v}`)}
              </Button>
            ))}
          </Space>
        ) : (
          <Button size="small" type="text" onClick={() => review(r, PENDING)}>
            {t('review.reopen')}
          </Button>
        ),
    },
  ]

  const filters = useMemo(
    () => (
      <Space wrap>
        <Select
          allowClear
          style={{ width: 150 }}
          placeholder={t('review.status')}
          value={status}
          onChange={setStatus}
          options={(data?.statuses ?? []).map((v) => ({
            value: v,
            label: `${v}${counts[v] ? ` (${counts[v]})` : ''}`,
          }))}
        />
        <Select
          allowClear
          style={{ width: 150 }}
          placeholder={t('review.type')}
          value={itype}
          onChange={setIType}
          options={(data?.itypes ?? []).map((v) => ({ value: v, label: v }))}
        />
        <Input
          allowClear
          prefix={<SearchOutlined />}
          style={{ width: 240 }}
          placeholder={t('review.search')}
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        <Select
          style={{ width: 170 }}
          value={sort}
          onChange={setSort}
          options={[
            { value: 'due', label: t('review.sortDue') },
            { value: '', label: t('review.sortNewest') },
          ]}
        />
      </Space>
    ),
    [data, status, itype, q, sort, counts, t],
  )

  return (
    <Card
        title={t('review.title')}
        extra={total > 0 ? filters : undefined}
        styles={{ body: { paddingTop: 12 } }}
      >
        {err && <Alert type="error" showIcon message={err} style={{ marginBottom: 12 }} />}

        {!loading && total === 0 && !err ? (
          // Empty is the expected state until the workflow emits any. Say what is missing and
          // exactly what to send, so nobody has to read the API docs to find out.
          <Space direction="vertical" size={12} style={{ maxWidth: 720 }}>
            <Typography.Title level={5} style={{ margin: 0 }}>
              {t('review.emptyTitle')}
            </Typography.Title>
            <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
              {t('review.emptyBody')}
            </Typography.Paragraph>
            <pre
              style={{
                background: token.colorFillQuaternary,
                padding: 12,
                borderRadius: token.borderRadius,
                fontSize: 12,
                overflowX: 'auto',
              }}
            >
              {`POST /api/v1/reports
{
  "symbol": "600519", "date": "2026-07-31", "subtype": "投资决策",
  "title": "…", "body_md": "…",
  "tracking": [
    { "itype": "assumption",
      "content": "毛利率维持 20%",
      "review_point": "2026-10-31 三季报" }
  ]
}`}
            </pre>
            <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
              {t('review.emptyHint')}
            </Typography.Paragraph>
          </Space>
        ) : (
          <Table<TrackingRow>
            rowKey="id"
            size="small"
            loading={loading}
            dataSource={data?.items ?? []}
            columns={columns}
            pagination={{ pageSize: 20, showSizeChanger: false, total: data?.total ?? 0 }}
          />
        )}
    </Card>
  )
}
