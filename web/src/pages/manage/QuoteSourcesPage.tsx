import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, InputNumber, Space, Switch, Table, Tag, Typography } from 'antd'
import { ClearOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import LoadGate from '../../components/LoadGate'
import StickyActionBar from '../../components/StickyActionBar'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'

// 管理 → 行情源: the console for the two compiled-in quote vendors.
//
// ADR 0028 §9 shipped quotes with no setting at all, and half of that argument has not survived
// contact with an admin: the reasoning against admin-editable URLs holds exactly and says nothing
// about the rest. Choosing among sources that are already compiled in is safe and useful; typing a
// host is neither. So this page edits the failover order, which sources are on, and the two cache
// TTLs — and shows the per-source health counters that quote_cache.go has been keeping since it was
// written while exposing them nowhere (its noteSuccess/noteFailure comment names this panel as the
// reader they were written for).
//
// The URLs are absent BY DESIGN, and the page says so in one line rather than leaving a gap where a
// field should be (quoteAdmin.whyNoUrl): a source here is a PARSER reading fixed field positions
// behind a drift gate, so pointing one at another host produces a gate refusal — `quote_unavailable`
// — rather than another source. An admin who goes looking for that field deserves the reason.

// The wire shape of GET/POST /api/admin/quote. Declared here rather than in api/types.ts because the
// handler and this page land in the same change from two directions and that file is being edited
// concurrently; fold it in once both are on the branch.
type QuoteAdminSource = {
  source: string
  enabled: boolean
  /** 1-based place in the failover order. Absent from the order setting = disabled. */
  position: number
  markets: string[]
  lastSuccess: string
  lastError: string
  lastErrorAt: string
  consecutiveFailures: number
}

type QuoteAdminState = {
  sources: QuoteAdminSource[]
  cacheEntries: number
  cacheBytes: number
  ttlOpenSecs: number
  ttlClosedSecs: number
  ttlOpenFloor: number
  ttlClosedFloor: number
}

// Go's time.Time has no empty form on the wire: a source that has never answered arrives as the zero
// instant "0001-01-01T00:00:00Z", not as "" and not as null, so rendering it verbatim tells an
// operator the source last succeeded in the year 1. Anything older than this portal is "never".
function stampOrNever(ts: string | undefined): string {
  if (!ts) return ''
  const d = dayjs(ts)
  if (!d.isValid() || d.year() < 2000) return ''
  return d.format('YYYY-MM-DD HH:mm:ss')
}

// An emptied box is a number being retyped, not a request for a value. Writing a fallback into state
// the moment it goes empty puts that fallback back under the cursor mid-edit, so clearing 30 and
// typing "2" leaves 52 — which then goes to the server as the cache TTL. Nulls are ignored instead:
// the last number stands, and an empty box at blur falls back to it.
function setTtlNumber(set: (n: number) => void, v: number | string | null) {
  if (typeof v === 'number') set(v)
}

// A number off the wire, or 0 when the body did not carry one.
//
// Not cosmetic, and not the same as `?? 0` on a counter: three of the four callers feed an antd
// InputNumber, and `value={undefined}` silently switches that control from CONTROLLED to
// uncontrolled — after which React stops writing to it and the box keeps whatever was last typed,
// including through the clamp read-back that save() exists for. The floors feed the hint beside it,
// which would otherwise read "≥ undefined". Only a malformed body reaches here, which is exactly
// the moment this page must not start inventing state it cannot see.
function wireNumber(v: unknown): number {
  return typeof v === 'number' && Number.isFinite(v) ? v : 0
}

// fmtBytes renders an approximate byte count in human units (as on the storage console — this is the
// same kind of number: an occupancy estimate, not an allocation).
function fmtBytes(n: number): string {
  if (!n || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = n
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}

export default function QuoteSourcesPage() {
  const { t } = useTranslation()
  const { message } = App.useApp()

  const [rows, setRows] = useState<QuoteAdminSource[]>([])
  const [cacheEntries, setCacheEntries] = useState(0)
  const [cacheBytes, setCacheBytes] = useState(0)
  const [ttlOpen, setTtlOpen] = useState(0)
  const [ttlClosed, setTtlClosed] = useState(0)
  const [ttlOpenFloor, setTtlOpenFloor] = useState(0)
  const [ttlClosedFloor, setTtlClosedFloor] = useState(0)
  const [loading, setLoading] = useState(true)
  // Separate from `loading` for the reason the storage console records: the reload behind a save or
  // a cache purge must refresh in place. Replacing a page that has just saved with a full-page load
  // error, because the refresh behind it happened to fail, throws away the form the admin is reading.
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')

  const apply = (st: QuoteAdminState) => {
    // Sorted by the server's own position field rather than trusted as it arrives. The failover
    // order IS the setting being edited here, while the health half of each row comes from
    // snapshotHealth, which walks a Go map and sorts it by source NAME — so a page that read the
    // order off the array's order is one refactor away from calling sina the primary because 's'
    // sorts before 't'.
    setRows([...(st.sources ?? [])].sort((a, b) => a.position - b.position))
    setCacheEntries(wireNumber(st.cacheEntries))
    setCacheBytes(wireNumber(st.cacheBytes))
    setTtlOpen(wireNumber(st.ttlOpenSecs))
    setTtlClosed(wireNumber(st.ttlClosedSecs))
    setTtlOpenFloor(wireNumber(st.ttlOpenFloor))
    setTtlClosedFloor(wireNumber(st.ttlClosedFloor))
  }

  const load = () => {
    setLoading(true)
    setLoadErr('')
    return api
      .get<QuoteAdminState>('/api/admin/quote')
      .then((st) => {
        apply(st)
        setLoaded(true)
      })
      .catch((e) => setLoadErr(errText(e, t)))
      .finally(() => setLoading(false))
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const save = async () => {
    // One field carries both the order and the on/off flags because one SETTING does:
    // quote_source_order is a comma list, and a source missing from it is disabled. Sending the
    // enabled sources in display order is therefore the whole of the writer.
    const order = rows
      .filter((r) => r.enabled)
      .map((r) => r.source)
      .join(',')
    // A BLANK order is not "no sources" — it is the server's reset-to-default signal
    // (quote_admin_api.go), so posting one writes "tencent,sina" and switches everything back on.
    // The admin would get a green "saved", watch both switches flip up, and have no way to tell
    // that their setting was not merely rejected but INVERTED. Same argument as the TTL clamp
    // below: a setting must never read as accepted when it was not. Here the write can simply be
    // refused, which is better than explaining it afterwards.
    //
    // setEnabled already keeps the switches out of this state, so the only way to arrive here is a
    // body in which the server itself enabled nothing — an order setting naming a source that no
    // longer exists does exactly that.
    if (!order) {
      message.warning(t('quoteAdmin.orderHint'))
      return
    }
    try {
      const st = await api.post<QuoteAdminState>('/api/admin/quote', {
        order,
        ttlOpenSecs: ttlOpen,
        ttlClosedSecs: ttlClosed,
      })
      message.success(t('common.saved'))
      // The server CLAMPS both TTLs to their floors, so the value now in force is the one in the
      // ANSWER, never the one that was typed. Leaving the typed number on screen under a green
      // "saved" is this page telling an admin the cache holds a quote for 1 second while the server
      // holds it for 5 — a setting that reads as accepted and was not.
      if (st && Array.isArray(st.sources)) apply(st)
      else await load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const clearCache = async () => {
    try {
      const st = await api.post<QuoteAdminState>('/api/admin/quote/cache/clear')
      message.success(t('quoteAdmin.cleared'))
      // The occupancy beside the button is the only evidence the button did anything, so it is read
      // back rather than assumed to be zero: the cache refills the instant the next reader opens a
      // stock page, and a hard-coded 0 would be a number this page made up.
      if (st && Array.isArray(st.sources)) apply(st)
      else await load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  // Switching the LAST enabled source off is refused at the click rather than corrected after the
  // save, because the save cannot correct it: an empty order resets the server to its shipped
  // default (see save()), so the sequence an admin actually gets is "saved", both switches back up,
  // and no explanation. The state is prevented instead of reverted, and the refusal says why with
  // the same sentence printed above the table — a disabled source is never called, so a page with
  // none enabled is a quote endpoint with nothing behind it.
  const setEnabled = (source: string, on: boolean) => {
    if (!on && rows.filter((r) => r.enabled).length <= 1) {
      message.warning(t('quoteAdmin.orderHint'))
      return
    }
    setRows((prev) => prev.map((r) => (r.source === source ? { ...r, enabled: on } : r)))
  }

  // Primary/fallback is a POSITION among the ENABLED sources, not a property of a source: switching
  // the first one off promotes the second, and the tags have to move with it or the page describes a
  // failover order the fetch path does not use.
  const enabledOrder = rows.filter((r) => r.enabled).map((r) => r.source)

  // i18next echoes a key it has no string for, so a third source added to the Go slice before its
  // label lands in the bundles would render as "quote.source.xxx". Its own name is a worse label and
  // a better failure.
  const sourceLabel = (name: string) => {
    const label = t(`quote.source.${name}`)
    return label === `quote.source.${name}` ? name : label
  }

  const columns = [
    {
      key: 'drag',
      title: '',
      width: 44,
      align: 'center' as const,
      // Ordering is the only mechanism here, so the handle carries dnd-kit's keyboard activator (see
      // dnd.tsx): the list is reorderable without a mouse, not merely announced as such.
      render: () => <DragHandle label={t('common.reorder')} />,
    },
    {
      key: 'source',
      title: t('quoteAdmin.sources'),
      render: (_: unknown, r: QuoteAdminSource) => (
        <Space wrap size={6}>
          <Typography.Text strong>{sourceLabel(r.source)}</Typography.Text>
          {r.enabled ? (
            enabledOrder[0] === r.source ? (
              <Tag color="blue">{t('quoteAdmin.primary')}</Tag>
            ) : (
              <Tag>{t('quoteAdmin.fallback')}</Tag>
            )
          ) : null}
        </Space>
      ),
    },
    {
      key: 'enabled',
      title: t('quoteAdmin.enabled'),
      width: 90,
      render: (_: unknown, r: QuoteAdminSource) => (
        <Switch
          checked={r.enabled}
          onChange={(v) => setEnabled(r.source, v)}
          // Named per row: every switch on this page otherwise announces the same word, and the
          // difference between them is the whole decision.
          aria-label={`${sourceLabel(r.source)} ${t('quoteAdmin.enabled')}`}
        />
      ),
    },
    {
      key: 'markets',
      title: t('quoteAdmin.markets'),
      render: (_: unknown, r: QuoteAdminSource) => (
        <Space wrap size={4}>
          {(r.markets ?? []).map((m) => (
            <Tag key={m}>{t(`quote.market.${m}`)}</Tag>
          ))}
        </Space>
      ),
    },
    {
      key: 'lastSuccess',
      title: t('quoteAdmin.lastSuccess'),
      render: (_: unknown, r: QuoteAdminSource) => {
        const at = stampOrNever(r.lastSuccess)
        return at ? <Typography.Text>{at}</Typography.Text> : <Typography.Text type="secondary">{t('quoteAdmin.never')}</Typography.Text>
      },
    },
    {
      key: 'failures',
      title: t('quoteAdmin.failures'),
      width: 130,
      render: (_: unknown, r: QuoteAdminSource) =>
        r.consecutiveFailures > 0 ? (
          <Tag color="red">{r.consecutiveFailures}</Tag>
        ) : stampOrNever(r.lastSuccess) ? (
          // Both halves are required for OK. A source nobody has called yet has no failures either,
          // and a green OK beside "last success: never" is this page inventing a health check that
          // never ran — which is the state a source switched off months ago is actually in.
          <Tag color="green">{t('quoteAdmin.ok')}</Tag>
        ) : null,
    },
    {
      key: 'lastError',
      title: t('quoteAdmin.lastError'),
      render: (_: unknown, r: QuoteAdminSource) => {
        if (!r.lastError) return null
        const at = stampOrNever(r.lastErrorAt)
        return (
          <Space direction="vertical" size={0}>
            {/* Verbatim, and never truncated to a code: what an operator needs off this page is the
                vendor's own sentence — "drift gate" and "connection refused" call for opposite
                responses, and only this string tells them apart. */}
            <Typography.Text type="danger">{r.lastError}</Typography.Text>
            {/* And WHEN it happened, which is the other half of the decision: the same sentence four
                minutes ago and last April are an incident and a scar, and the failure counter beside
                it does not separate them — consecutiveFailures resets on the next success while this
                string stays, so a source reading 正常 can still be carrying a months-old error here.
                No "never" fallback, unlike 最近成功: a source with no error to date renders an empty
                cell, whereas a source that has never SUCCEEDED is itself the finding and says so. */}
            {at ? (
              <Typography.Text type="secondary" data-testid="quote-source-error-at" style={{ fontSize: 12 }}>
                {at}
              </Typography.Text>
            ) : null}
          </Space>
        )
      },
    },
  ]

  const row = (label: string, control: React.ReactNode, hint?: string) => (
    <Space wrap>
      <span style={{ display: 'inline-block', minWidth: 180 }}>{label}</span>
      {control}
      {hint ? <Typography.Text type="secondary">{hint}</Typography.Text> : null}
    </Space>
  )

  // The floor belongs to the SERVER, and it is stated here rather than enforced by the control:
  // min={floor} would only hide the clamp, and what an admin must end up looking at is the number the
  // server is actually running (see save()). The frozen hint carries no slot for a number, so the
  // bound is appended as notation — which reads the same in all three bundles.
  const ttlHint = (floor: number) => `${t('quoteAdmin.ttlHint')} ≥ ${floor}`

  return (
    <LoadGate loading={loading && !loaded} error={loaded ? undefined : loadErr} onRetry={load}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {t('quoteAdmin.title')}
        </Typography.Title>

        <Card title={t('quoteAdmin.sources')}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <Alert type="info" showIcon message={t('quoteAdmin.whyNoUrl')} />
            <Typography.Text type="secondary">{t('quoteAdmin.orderHint')}</Typography.Text>
            <SortableWrapper
              ids={rows.map((r) => r.source)}
              onReorder={(order) =>
                setRows(
                  order
                    .map((k) => rows.find((r) => r.source === k))
                    .filter((r): r is QuoteAdminSource => !!r),
                )
              }
            >
              <Table<QuoteAdminSource>
                rowKey="source"
                size="small"
                dataSource={rows}
                pagination={false}
                components={sortableTableComponents}
                columns={columns}
                scroll={{ x: 900 }}
              />
            </SortableWrapper>
          </div>
        </Card>

        <Card title={t('quoteAdmin.cache')}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <Space wrap size={16} align="center">
              <span>
                <Typography.Text type="secondary">{t('quoteAdmin.cacheEntries')}</Typography.Text>{' '}
                <Typography.Text strong>{cacheEntries}</Typography.Text>
              </span>
              <span>
                <Typography.Text type="secondary">{t('quoteAdmin.cacheBytes')}</Typography.Text>{' '}
                <Typography.Text strong>{fmtBytes(cacheBytes)}</Typography.Text>
              </span>
              {/* No confirm: the cache is memory-only and evaporates on restart anyway (ADR 0028 §1),
                  so the worst this button can cost is one vendor call per symbol somebody reopens. */}
              <Button icon={<ClearOutlined />} onClick={clearCache}>
                {t('quoteAdmin.clearCache')}
              </Button>
            </Space>

            {row(
              t('quoteAdmin.ttlOpen'),
              <InputNumber
                min={1}
                value={ttlOpen}
                onChange={(v) => setTtlNumber(setTtlOpen, v)}
                aria-label={t('quoteAdmin.ttlOpen')}
              />,
              ttlHint(ttlOpenFloor),
            )}
            {row(
              t('quoteAdmin.ttlClosed'),
              <InputNumber
                min={1}
                value={ttlClosed}
                onChange={(v) => setTtlNumber(setTtlClosed, v)}
                aria-label={t('quoteAdmin.ttlClosed')}
              />,
              ttlHint(ttlClosedFloor),
            )}

            <StickyActionBar>
              <Button type="primary" onClick={save}>
                {t('common.save')}
              </Button>
            </StickyActionBar>
          </div>
        </Card>
      </div>
    </LoadGate>
  )
}
