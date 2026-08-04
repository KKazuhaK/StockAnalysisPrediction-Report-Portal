import { useCallback, useEffect, useState } from 'react'
import { auditTime } from '../../lib/auditTime'
import { formatRegion } from '../../lib/geo'
import { Alert, Card, DatePicker, Input, Select, Space, Table, Tag, Tooltip, Typography } from 'antd'
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
// Colour by CONSEQUENCE, not by subsystem: a reader scanning a page should see refusals and
// access changes without reading them. Red is "somebody's access changed or somebody was turned
// away", orange is "an account or an OU was edited", purple is configuration, blue is a read,
// green is an ordinary successful sign-in, and everything unlisted is grey.
const ACTION_COLOR: Record<string, string> = {
  'report.read': 'blue',
  'grant.change': 'red',
  'user.change': 'orange',
  'group.change': 'orange',
  'policy.change': 'purple',

  'auth.login': 'green',
  'auth.login_failed': 'red',
  'auth.lockout': 'red',
  'auth.logout': 'default',
  'auth.password_change': 'orange',
  'auth.password_reset': 'orange',
  'auth.mfa_change': 'red',
  'auth.identity_link': 'red',
  'auth.identity_unlink': 'red',

  'user.create': 'orange',
  'user.delete': 'red',
  'group.create': 'orange',
  'group.delete': 'red',

  'report.ingest': 'cyan',
  'report.delete': 'red',

  'run.submit': 'geekblue',
  'run.cancel': 'default',
  'run.change': 'default',
  'run.delete': 'default',

  'token.create': 'red',
  'token.delete': 'default',
  'app.install': 'purple',
  'app.delete': 'default',
  'webhook.create': 'red',
  'webhook.delete': 'default',
  'target.change': 'purple',
}

/**
 * The IP database: what is loaded, where to put one, and where to fetch one from.
 *
 * It lives here rather than in a settings page because this is the only screen that shows an IP
 * address — somebody wondering why a row has no location is looking at the row.
 *
 * The source URL is write-only. Every vendor puts a credential in its query string, so the server
 * reports only WHETHER one is configured; the field starts blank and blank means "leave it alone",
 * the same rule the SMTP password and the SSO client secrets already follow.
 */
export default function AuditPage() {
  const { t } = useTranslation()
  const [data, setData] = useState<AuditResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [action, setAction] = useState<string>()
  const [actor, setActor] = useState('')
  const [ip, setIP] = useState('')
  const [q, setQ] = useState('')
  const [since, setSince] = useState<string>()
  const [page, setPage] = useState(1)

  const pageSize = 50

  const load = useCallback(() => {
    setLoading(true)
    const qs = new URLSearchParams({ limit: String(pageSize), offset: String((page - 1) * pageSize) })
    if (action) qs.set('action', action)
    if (actor.trim()) qs.set('actor', actor.trim())
    if (ip.trim()) qs.set('ip', ip.trim())
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
  }, [action, actor, ip, q, since, page, t])
  useEffect(load, [load])

  const ouNames = data?.ou_names ?? {}
  const geo = data?.geo

  const columns: ColumnsType<AuditEntry> = [
    {
      title: t('audit.at'),
      dataIndex: 'at',
      width: 190,
      // The panel timezone, with the reader's own beneath it only when the two differ — an operator
      // abroad reading a log about a business day elsewhere needs both, and everyone else needs one.
      render: (v: string) => {
        const at = auditTime(v, data?.timezone ?? '')
        return (
          <Space direction="vertical" size={0}>
            <Typography.Text style={{ fontSize: 12 }}>{at.text}</Typography.Text>
            {at.local && (
              <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                {t('audit.yourTime', { at: at.local })}
              </Typography.Text>
            )}
            {at.legacy && (
              <Tooltip title={t('audit.legacyTimeHint')}>
                <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                  {t('audit.legacyTime')}
                </Typography.Text>
              </Tooltip>
            )}
          </Space>
        )
      },
    },
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
          {/* Under the actor, because on a failed sign-in it is the only identity there is: no
              account has authenticated, and the address is who to look at. Click to filter. */}
          {r.ip && (
            <Space size={4} wrap>
              <Typography.Link
                style={{ fontSize: 11 }}
                onClick={() => {
                  setIP(r.ip ?? '')
                  setPage(1)
                }}
              >
                {r.ip}
              </Typography.Link>
              {/* Only when a database resolved it. No database, a LAN address, or an
                  address nobody has mapped all render as the bare address. */}
              {formatRegion(r.geo) && (
                <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                  {formatRegion(r.geo)}
                </Typography.Text>
              )}
            </Space>
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
          {/* An equality filter, not the free-text one. "Which host tried nine accounts" is the
              query that turns a pile of failed sign-ins into a single incident. */}
          <Input
            allowClear
            style={{ width: 150 }}
            placeholder={t('audit.ipFilter')}
            value={ip}
            onChange={(e) => {
              setIP(e.target.value)
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
      {/* A footnote, not a banner: it is reference rather than a problem. Without it, "no IP
          database installed" and "installed, but nothing public has shown up yet" look identical,
          because both render as bare addresses. */}
      {/* Louder than the geo footnote, because it means the column is actively misleading: every
          row shows the proxy's address, and it looks exactly like real data. */}
      {data?.proxy_hint && (
        <Alert
          type="warning"
          showIcon
          style={{ marginTop: 16 }}
          message={t('audit.proxyHintTitle')}
          description={t('audit.proxyHintBody')}
        />
      )}
      {/* A footnote only: the controls live in 常规, but this is the page where somebody notices
          a row has no location, so it says what the state is. */}
      {geo && (
        <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: 12 }}>
          {!geo.enabled
            ? t('audit.geoOff')
            : geo.loaded
              ? t('audit.geoLoaded', {
                  file: geo.file,
                  type: geo.info?.type || '—',
                  built: geo.info?.build_epoch
                    ? new Date(geo.info.build_epoch * 1000).toISOString().slice(0, 10)
                    : '—',
                })
              : t('audit.geoMissingShort')}
        </Typography.Text>
      )}
    </Card>
  )
}
