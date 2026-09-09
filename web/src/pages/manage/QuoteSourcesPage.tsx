import { useEffect, useState } from 'react'
import { Alert, App, Button, Card, InputNumber, Space, Switch, Table, Tag, Typography } from 'antd'
import { ClearOutlined } from '@ant-design/icons'
import dayjs from 'dayjs'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import LoadGate from '../../components/LoadGate'
import StickyActionBar from '../../components/StickyActionBar'
import { DragHandle, SortableWrapper, sortableTableComponents } from './dnd'

// 管理 → 行情源: the console for the compiled-in quote vendors.
//
// ADR 0028 §9 shipped quotes with no setting at all, and half of that argument has not survived
// contact with an admin: the reasoning against admin-editable URLs holds exactly and says nothing
// about the rest. Choosing among sources that are already compiled in is safe and useful; typing a
// host is neither. So this page edits the failover order, which sources are on, the three cache
// TTLs, whether the home feed asks for prices, and whether visible pages repeat those requests. It
// also shows the per-source health counters that quote_cache.go has been keeping since it was
// written while exposing them nowhere (its noteSuccess/noteFailure comment names this panel as the
// reader they were written for).
//
// What the order MEANS is a capability table, not a preference: the resolver walks the enabled
// sources in this order and skips any that has not declared the (market, interval) pair in hand, so
// a source can sit at the top of the list and never be called for a US chart. That is why each row
// prints what it declares — an operator dragging Yahoo above Tencent is not choosing a vendor for
// everything, they are choosing one for US dailies and intraday, and a panel that showed only names
// and an order would make that move look like something it is not.
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
  /**
   * Every market this source can say ANYTHING about — the union over the intervals below, which is
   * what the server's marketIDs() computes and the only honest reading of a single list.
   *
   * It is therefore a wider claim than the two lists beside it, and the US is the case that proves
   * the difference matters: Tencent names `us` here and cannot draw it, because a sixty-bar request
   * for usAAPL comes back with two rows fifteen years apart (ADR 0030 §4). A panel that showed only
   * this column would tell an operator moving a source that Tencent serves the US — true of the
   * price, false of the chart, and the reason the capability column exists.
   */
  markets: string[]
  /** Markets this source serves a DAILY series for. Empty is a real answer, not a missing one. */
  daily: string[]
  /** Markets it serves ONE SESSION of minutes for — 分时, and not the five-day window below. */
  intraday: string[]
  /**
   * Markets it serves the FIVE-SESSION window for.
   *
   * Separate from `intraday` because they are separate claims, and the panel said otherwise until a
   * reader found out the hard way: the server used to union the two under one 分时 column, so a
   * source that served only the one-day window was printed as covering both, and 5日 looked
   * available in three markets while every 5日 request in every market degraded to a snapshot.
   */
  intraday5d: string[]
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
  ttlIntradaySecs: number
  ttlOpenFloor: number
  ttlClosedFloor: number
  ttlIntradayFloor: number
  /** Whether the home feed asks for prices at all. See the card at the bottom of the page. */
  homeCards: boolean
  /** Whether visible quote surfaces follow the server's session-aware refresh advice. */
  autoRefresh: boolean
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

// Where a row sits in the failover chain, for sorting only.
//
// A source absent from the order setting has position 0 — and 0 sorts BEFORE 1, so a plain
// `a.position - b.position` seats every DISABLED source above the primary. That was invisible while
// both compiled-in sources shipped enabled and stopped being invisible the moment one shipped OFF:
// Yahoo would have opened this page sitting on top of a chain it is not part of, and — because the
// order written back is the row order — switching it on would have promoted it to primary in the
// same gesture, quietly routing every A-share request to an undocumented endpoint. Disabled sources
// sort last instead, so enabling one APPENDS it to the chain and moving it up stays a drag the
// operator performs on purpose.
function seat(r: QuoteAdminSource): number {
  return r.position > 0 ? r.position : Number.MAX_SAFE_INTEGER
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
  const [ttlIntraday, setTtlIntraday] = useState(0)
  const [ttlOpenFloor, setTtlOpenFloor] = useState(0)
  const [ttlClosedFloor, setTtlClosedFloor] = useState(0)
  const [ttlIntradayFloor, setTtlIntradayFloor] = useState(0)
  const [homeCards, setHomeCards] = useState(false)
  // Whether the value above is anybody's ANSWER — the GET carried the key, or the operator moved
  // the switch — rather than this page's fail-closed stand-in for a body that did not mention it.
  // save() sends the key only when this is true; see the comment on the payload.
  const [homeCardsKnown, setHomeCardsKnown] = useState(false)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [autoRefreshKnown, setAutoRefreshKnown] = useState(false)
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
    setRows([...(st.sources ?? [])].sort((a, b) => seat(a) - seat(b)))
    setCacheEntries(wireNumber(st.cacheEntries))
    setCacheBytes(wireNumber(st.cacheBytes))
    setTtlOpen(wireNumber(st.ttlOpenSecs))
    setTtlClosed(wireNumber(st.ttlClosedSecs))
    setTtlIntraday(wireNumber(st.ttlIntradaySecs))
    setTtlOpenFloor(wireNumber(st.ttlOpenFloor))
    setTtlClosedFloor(wireNumber(st.ttlClosedFloor))
    setTtlIntradayFloor(wireNumber(st.ttlIntradayFloor))
    // Strictly `=== true`, and not `?? true` on the shipped default. A body this page cannot read
    // is exactly where it must not assume consent: switching this on sends the codes on screen to a
    // third-party vendor on every home page view, so an unreadable answer renders as OFF and the
    // operator turns it on deliberately. The other direction would have the page inventing a
    // disclosure nobody agreed to and then writing it back on the next save.
    //
    // Reading OFF is half of that; the other half is NOT WRITING IT BACK, which is what the flag
    // beside it carries. An answer with no `homeCards` is an older server behind a newer bundle
    // (the shape a rolling deploy serves for a few minutes), and the admin who opened this page in
    // that window came to change a TTL. Posting the unread OFF would switch a default-ON disclosure
    // off in their name, under a green "已保存", with nothing on screen admitting it — the TTLs
    // survive the same round trip only because the server clamps them, and this key has no clamp.
    setHomeCardsKnown(typeof st.homeCards === 'boolean')
    setHomeCards(st.homeCards === true)
    setAutoRefreshKnown(typeof st.autoRefresh === 'boolean')
    setAutoRefresh(st.autoRefresh === true)
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
    // Every setting on the page, except that `homeCards` is sent only when there is something to
    // send: an operator who moved the switch, or a GET that carried the key. A body that did not
    // mention it leaves the switch reading OFF (see apply()), and posting THAT back is how a
    // default-ON disclosure gets switched off by an admin who came to edit a cache TTL. Omitting
    // the key writes nothing and leaves the server's own value in force, which is the only honest
    // thing a page that never learned the value can do with it.
    const body: Record<string, unknown> = {
      order,
      ttlOpenSecs: ttlOpen,
      ttlClosedSecs: ttlClosed,
      ttlIntradaySecs: ttlIntraday,
    }
    if (homeCardsKnown) body.homeCards = homeCards
    if (autoRefreshKnown) body.autoRefresh = autoRefresh
    try {
      const st = await api.post<QuoteAdminState>('/api/admin/quote', body)
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

  // What an operator has to know about a particular vendor before switching it on, beside its
  // switch. Keyed on the source NAME rather than sent by the server, because it is a fact about the
  // vendor rather than about this build's state — and keyed rather than applied to every disabled
  // row, so a fourth source does not silently inherit Yahoo's licensing disclaimer. A source with
  // nothing to declare gets no box; an empty one would read as a warning with the text missing.
  const sourceNotice = (name: string): string => (name === 'yahoo' ? t('quoteAdmin.yahooNotice') : '')

  // One line of the capability column: an interval, and the markets this source declares for it.
  //
  // An empty list renders as a DASH rather than as nothing. "Serves no intraday" and "the server
  // sent no capability field" would otherwise be the same blank cell, and the first is the fact an
  // operator is on this page to read — the whole reason Yahoo is worth enabling is that its daily
  // line names a market the others' do not.
  const capLine = (label: string, markets: string[] | undefined, testid: string) => (
    <div data-testid={testid}>
      <Space wrap size={4}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {label}
        </Typography.Text>
        {(markets ?? []).length ? (
          (markets ?? []).map((m) => <Tag key={m}>{t(`quote.market.${m}`)}</Tag>)
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        )}
      </Space>
    </div>
  )

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
        <div data-testid="quote-source-markets">
          <Space wrap size={4}>
            {(r.markets ?? []).map((m) => (
              <Tag key={m}>{t(`quote.market.${m}`)}</Tag>
            ))}
          </Space>
        </div>
      ),
    },
    {
      key: 'capabilities',
      title: t('quoteAdmin.capabilities'),
      // The markets column above answers "can this source PRICE this market"; these two answer
      // "and can it draw it, at which resolution". They are separate columns because the answers
      // differ, per market and per source, and the resolver acts on the pair rather than on the
      // union — a source is skipped for a (market, interval) it has not declared no matter where the
      // operator drags it. Without this column the order is a list of names in a sequence whose
      // effect cannot be predicted from anything on screen.
      render: (_: unknown, r: QuoteAdminSource) => (
        <Space direction="vertical" size={2}>
          {capLine(t('quoteAdmin.daily'), r.daily, 'quote-source-daily')}
          {capLine(t('quoteAdmin.intraday'), r.intraday, 'quote-source-intraday')}
          {/* A third LINE in the same cell rather than a fourth column: the table is already wide
              enough to scroll, and what an operator needs is to see the two windows differ, not to
              sort by either. The label is the range strip's own 5日 — the reader and the operator
              are looking at the same button. */}
          {capLine(t('quote.range.5d'), r.intraday5d, 'quote-source-intraday5d')}
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
            {/* The vendor notices, at the CARD's width rather than inside a table cell.
                They used to sit in the source column under a 360px cap, where a three-sentence
                licensing notice wrapped to five lines and pushed its row to roughly two hundred
                pixels tall — one vendor's disclaimer setting the height of a table an operator
                came to read the other rows of. The text is unchanged and still shown by default:
                it is the one thing on this panel that is not a status, and hiding it behind a
                tooltip would mean an operator who never hovers decides without it. Named per
                vendor, because at this width it is no longer beside its own row. */}
            {rows.map((r) =>
              sourceNotice(r.source) ? (
                <Alert
                  key={r.source}
                  data-testid="quote-source-notice"
                  type="warning"
                  showIcon
                  message={`${sourceLabel(r.source)}：${sourceNotice(r.source)}`}
                />
              ) : null,
            )}
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
                // Wider than the 900 this table shipped with: it has gained columns of market tags,
                // and columns that cram are how a row's two market lists start looking like one.
                // Below this width the table scrolls. The vendor notices are NOT in here — they are
                // alerts above the table, at the card's width, for exactly this reason.
                scroll={{ x: 1100 }}
              />
            </SortableWrapper>
          </div>
        </Card>

        <Card title={t('quoteAdmin.cache')}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {row(
              t('quoteAdmin.autoRefresh'),
              <Switch
                checked={autoRefresh}
                onChange={(on) => {
                  setAutoRefresh(on)
                  setAutoRefreshKnown(true)
                }}
                aria-label={t('quoteAdmin.autoRefresh')}
              />,
              t('quoteAdmin.autoRefreshHint'),
            )}
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
            {/* Its own TTL, and not a third way of saying the first one: the open/closed pair is
                about whether the market is moving, while this is about the RESOLUTION of what was
                asked for. A one-minute series cached for the five minutes that suit a daily chart
                is four minutes of a chart that claims to be minute-by-minute and is not — the
                failure looks like data rather than like staleness, which is why it gets a number of
                its own and a floor (15) higher than the open one. */}
            {row(
              t('quoteAdmin.ttlIntraday'),
              <InputNumber
                min={1}
                value={ttlIntraday}
                onChange={(v) => setTtlNumber(setTtlIntraday, v)}
                aria-label={t('quoteAdmin.ttlIntraday')}
              />,
              ttlHint(ttlIntradayFloor),
            )}
          </div>
        </Card>

        {/* A disclosure, not a display preference, which is why it is a switch on this page rather
            than a default nobody was told about: with it on, every home page view sends the codes
            currently on screen to a third-party vendor. The hint says exactly that, in the open —
            an operator cannot weigh what they have not been shown, and hiding the sentence behind a
            question mark next to a switch that ships ON would be this page keeping it from them. */}
        <Card title={t('quoteAdmin.homeCards')}>
          {/* alignItems is not a nicety here. A column flex container stretches its children by
              default, and a Switch that has been stretched is a 1900px blue bar across the card —
              it stops reading as a control at all, and its off state reads as a progress track that
              never filled. The switch is sized by its own content; only the hint below it may run
              the width of the card. */}
          <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'flex-start', gap: 8 }}>
            {/* Moving the switch is itself an answer, so it is what makes the value sendable: a
                key the GET did not carry is omitted from the save, but only until somebody
                decides it here — otherwise the control would be one an operator can move and
                cannot save. */}
            <Switch
              checked={homeCards}
              onChange={(on) => {
                setHomeCards(on)
                setHomeCardsKnown(true)
              }}
              aria-label={t('quoteAdmin.homeCards')}
            />
            <Typography.Text type="secondary">{t('quoteAdmin.homeCardsHint')}</Typography.Text>
          </div>
        </Card>

        {/* One bar for the whole page, outside the cards: one POST carries the order, the three
            TTLs and both quote switches, so a Save button inside the cache card would be a button
            that saves more than the card it sits in says it does. */}
        <StickyActionBar>
          <Button type="primary" onClick={save}>
            {t('common.save')}
          </Button>
        </StickyActionBar>
      </div>
    </LoadGate>
  )
}
