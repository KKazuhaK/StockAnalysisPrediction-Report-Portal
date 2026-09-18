import { Fragment, useCallback, useEffect, useState, type ReactNode } from 'react'
import { auditTime } from '../../lib/auditTime'
import { formatRegion } from '../../lib/geo'
import {
  Alert,
  Badge,
  Button,
  Card,
  DatePicker,
  Descriptions,
  Empty,
  Grid,
  Input,
  Modal,
  Pagination,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Tooltip,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { FilterOutlined, InfoCircleOutlined, SearchOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import dayjs from 'dayjs'
import { api, errText } from '../../api/client'
import { auditDetail, headline, listSeparator, type ChangeMember, type DetailPart } from '../../lib/auditDetail'
import type { AuditEntry, AuditResp } from '../../api/types'
import { clickable } from '../../lib/clickable'

// The subset of i18next's t() the detail helpers use. i18next's own type is overloaded in ways that
// make it awkward to pass down; this is the shape it is assignable to, and the shape both need —
// the same one runSchedule's summary helpers take.
type TFunc = (key: string, opts?: Record<string, unknown>) => string

// Actions recorded BEFORE anybody has authenticated.
//
// An empty actor means two different things, and the column must not fold them together. A token
// holder has no username — that really is a machine. A refused sign-in has no actor at all: nobody
// authenticated, so there is nobody to name, and the account that was TRIED is the only name the row
// carries. Printing "API token" for the second asserts a caller that never existed, and it buries
// the one thing an operator scanning for attacks is looking for — which account somebody tried.
const PRE_AUTH_ACTIONS = new Set(['auth.login_failed', 'auth.lockout'])

function actorOf(r: AuditEntry, t: TFunc): string {
  if (r.actor) return r.actor
  // The attempted account, which the server records as the target for exactly this reason: an
  // account's timeline is one target filter whether the actor was its holder or nobody.
  if (PRE_AUTH_ACTIONS.has(r.action)) return r.target_id || '—'
  return t('audit.machine')
}

// A pair of fields that is one change. Read as two fields it has to be diffed by eye — and the
// server stores them in a map, so they arrive alphabetically and "after" comes first.
//
// The direction is a WORD, not a sign. Every admin console worth copying labels it — Google's admin
// log pairs "Old value"/"New value", Entra names the modified property's old and new values — and a
// bare "+extuser" leaves the reader to work out both what changed and which way. The member's kind
// is named with it, because dropping the store's `u:`/`g:` prefix otherwise throws away the only
// thing that said whether "ext-demo" was an account or a unit.
function Change({ part, t }: { part: Extract<DetailPart, { kind: 'change' }>; t: TFunc }) {
  const members = (list: ChangeMember[]) =>
    list.map((m) => (m.type ? `${t(`audit.t.${m.type}`)} ${m.name}` : m.name)).join(listSeparator)
  const groups: ReactNode[] = []
  // Whether either direction was reported. Tracked separately from the group list, because the
  // label is a group too — testing the list for emptiness swallowed the two VALUES of every scalar
  // change and left a bare field label on the line.
  let diffed = false
  if (part.added.length) {
    diffed = true
    groups.push(
      <span>
        <span className="rp-audit-detail__add">{t('audit.diff.added')}</span> {members(part.added)}
      </span>,
    )
  }
  if (part.removed.length) {
    diffed = true
    groups.push(
      <span>
        <span className="rp-audit-detail__del">{t('audit.diff.removed')}</span> {members(part.removed)}
      </span>,
    )
  }
  // Sides that are not lists have no membership to report, so the change is the two values.
  if (!diffed) groups.push(<span>{`${part.from} → ${part.to}`}</span>)
  return (
    <>
      {/* The label names the change, so it is a PREFIX of the line rather than another thing on it —
          "label · was → is" reads as two facts where there is one. */}
      {part.key ? (
        <>
          <code>{part.key}</code>{' '}
        </>
      ) : null}
      {groups.map((g, i) => (
        <Fragment key={i}>
          {i > 0 && ' · '}
          {g}
        </Fragment>
      ))}
    </>
  )
}

// A detail is rendered as parts, not as one sentence: the lead reads as prose, a value from the
// server's closed vocabularies as a phrase, and every other field as an identifier beside its
// value. See lib/auditDetail.ts for why the distinction is the whole point.
//
// The identifier is monospace and dim, the value is not, so a reader's eye lands on the value and
// an unfamiliar field name costs nothing — which is what lets a field the server adds tomorrow be
// readable in every language without a translation table nobody would keep up.
function DetailParts({ parts, full, t }: { parts: DetailPart[]; full?: boolean; t: TFunc }) {
  if (parts.length === 0) return <>{'—'}</>
  return (
    <div className={full ? 'rp-audit-detail rp-audit-detail--full' : 'rp-audit-detail'}>
      {parts.map((p, i) => (
        <Fragment key={i}>
          {/* Spaces rather than margins: this text is also what a copy-and-paste out of the column
              carries, and "before —" glued into "before—" is not what anybody meant to copy. */}
          {i > 0 && <span className="rp-audit-detail__sep"> · </span>}
          {p.kind === 'change' ? (
            <span className="rp-audit-detail__field">
              <Change part={p} t={t} />
            </span>
          ) : p.kind === 'field' ? (
            <span className="rp-audit-detail__field">
              <code>{p.key}</code> {p.value}
            </span>
          ) : (
            <span className="rp-audit-detail__text">{p.text}</span>
          )}
        </Fragment>
      ))}
    </div>
  )
}

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
  // A phone cannot hold six columns. antd does not refuse — it squeezes them until CJK wraps one
  // character per line and a row becomes a column of glyphs, which is worse than not showing the
  // table at all. On a narrow screen the same rows are rendered as cards: one row per card, each
  // field on its own line at full width, and the whole card opens the full record.
  //
  // lg rather than the md this codebase usually switches on, because this table is unusually wide
  // AND this page sits inside the manage layout, which keeps its sidebar from md up: a portrait
  // tablet has about 600px of content for a table that wants 1040, and cards read better there
  // than a table scrolled sideways with the detail column off the edge.
  const mobile = !Grid.useBreakpoint().lg
  const [data, setData] = useState<AuditResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [action, setAction] = useState<string>()
  const [row, setRow] = useState<AuditEntry | null>(null) // the row opened in full
  const [actor, setActor] = useState('')
  const [ip, setIP] = useState('')
  const [q, setQ] = useState('')
  const [since, setSince] = useState<string>()
  const [page, setPage] = useState(1)
  // Five filter controls at full width would push the first row off a phone screen, so all but
  // the search box fold away. The badge is what stops a folded-away filter from silently
  // explaining an empty page.
  const [filtersOpen, setFiltersOpen] = useState(false)

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
      // Data and the loading flag land in ONE state transition. Splitting them across .then and
      // .finally puts a render between them where the rows exist but the table is still blurred
      // and inert (antd's Spin sets pointer-events: none), which is a frame nobody sees in a
      // browser and a real race for anything that clicks a row as soon as it appears.
      .then((r) => {
        setData(r)
        setErr('')
        setLoading(false)
      })
      .catch((e) => {
        setErr(errText(e, t))
        setLoading(false)
      })
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
          <Space orientation="vertical" size={0}>
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
        <Space orientation="vertical" size={0}>
          {/* A machine caller has no username. Saying so beats an empty cell, which reads as a bug. */}
          <Typography.Text>{actorOf(r, t)}</Typography.Text>
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
                {...clickable(() => {
                  setIP(r.ip ?? '')
                  setPage(1)
                })}
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
      // A target_type is a closed vocabulary of our own writing, so it is NAMED rather than shown as
      // the identifier the table happens to store. An unknown one stays as it is, which is how a
      // type this build has not been taught still reads.
      title: t('audit.target'),
      width: 200,
      render: (_, r) => (
        <Typography.Text type="secondary">
          {t(`audit.t.${r.target_type}`, r.target_type)} {r.target_id}
        </Typography.Text>
      ),
    },
    {
      title: t('audit.detail'),
      // Rendered, not dumped: somebody reading this column wants "who read what", not the field
      // list a serialiser produced. And it is the HEADLINE only — a run's parameters, a token's
      // expiry and everything else an identifier carries are behind the full-record button, because
      // a page of rows holding every parameter of every submission is a wall nobody reads. The cell
      // is clamped to two lines as well, so one long line cannot stretch its row either.
      render: (_, r) => <DetailParts t={t} parts={headline(auditDetail(r.action, r.detail, t, { ouNames }))} />,
    },
    {
      // Scanning and investigating want opposite things from this table. The column above serves
      // the scan; this serves the investigation — every field as stored, including the ones the
      // sentence drops for being uninformative, and the payload verbatim for pasting elsewhere.
      title: '',
      width: 48,
      render: (_, r) => (
        <Button size="small" type="text" icon={<InfoCircleOutlined />} title={t('audit.details')} onClick={() => setRow(r)} />
      ),
    },
  ]

  // On a phone every control takes the full width of its cell; on a desktop each keeps the width
  // its content needs, so the header row stays one line.
  const width = (w: number) => (mobile ? { width: '100%' } : { width: w })

  // The narrowing filters, minus the free-text search — that one stays visible on a phone while
  // these fold away, because it is the one people reach for first.
  const narrowFilters = (
    <>
      <Select
        allowClear
        style={width(170)}
        placeholder={t('audit.action')}
        value={action}
        onChange={(v) => {
          setAction(v)
          setPage(1)
        }}
        // The label is a sentence ("changed a grant"), so on a phone the popup is wider than the
        // 50%-width control that opens it rather than truncating every option.
        popupMatchSelectWidth={false}
        // Built from the values present, so rows written by an older build still filter.
        options={(data?.actions ?? []).map((a) => ({ value: a, label: t(`audit.a.${a}`, a) }))}
      />
      <Input
        allowClear
        style={width(170)}
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
        style={width(150)}
        placeholder={t('audit.ipFilter')}
        value={ip}
        onChange={(e) => {
          setIP(e.target.value)
          setPage(1)
        }}
      />
      <DatePicker
        style={mobile ? { width: '100%' } : undefined}
        placeholder={t('audit.since')}
        value={since ? dayjs(since) : null}
        onChange={(d) => {
          setSince(d ? d.format('YYYY-MM-DD') : undefined)
          setPage(1)
        }}
      />
    </>
  )

  const search = (
    <Input
      allowClear
      prefix={<SearchOutlined />}
      style={width(220)}
      placeholder={t('audit.search')}
      value={q}
      onChange={(e) => {
        setQ(e.target.value)
        setPage(1)
      }}
    />
  )

  const items = data?.items ?? []
  const pager = (
    <Pagination
      align="center"
      // "1/14" rather than a row of page numbers: the numbered pager wraps to three lines on a
      // phone, and there is nothing on a page of audit rows worth jumping to by number.
      simple={mobile}
      current={page}
      pageSize={pageSize}
      total={data?.total ?? 0}
      showSizeChanger={false}
      onChange={setPage}
      style={{ marginTop: 16 }}
    />
  )

  // One row, as a card. Same fields as the table and in the same order, but each on its own line
  // at full width — the whole point is that nothing has to fit in a 40px column. The card itself
  // is the control that opens the full record; there is no room for a 48px button column, and a
  // tap target the size of the row is the one a thumb hits.
  const rowCard = (r: AuditEntry) => {
    const at = auditTime(r.at, data?.timezone ?? '')
    const region = formatRegion(r.geo)
    // The card is a bigger target than a table cell, not a bigger view: it shows the same headline
    // and opens the same record, so the two layouts cannot drift into telling different stories.
    const detail = headline(auditDetail(r.action, r.detail, t, { ouNames }))
    return (
      <div
        key={r.id}
        className="rp-audit-row"
        role="button"
        tabIndex={0}
        title={t('audit.details')}
        onClick={() => setRow(r)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            setRow(r)
          }
        }}
      >
        <div className="rp-audit-row__head">
          <Tag color={ACTION_COLOR[r.action]} style={{ marginInlineEnd: 0 }}>
            {t(`audit.a.${r.action}`, r.action)}
          </Tag>
          <Typography.Text type="secondary" className="rp-audit-row__when">
            {at.text}
          </Typography.Text>
        </div>
        <div className="rp-audit-row__meta">
          <Typography.Text style={{ fontSize: 13 }}>{actorOf(r, t)}</Typography.Text>
          {r.actor_ou > 0 && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {ouNames[String(r.actor_ou)] ?? `OU ${r.actor_ou}`}
            </Typography.Text>
          )}
          {r.ip && (
            <Typography.Link
              style={{ fontSize: 12 }}
              // The address filters the log; the card opens the record. Without this, tapping the
              // address does both and the modal covers the result.
              {...clickable((e) => {
                e.stopPropagation()
                setIP(r.ip ?? '')
                setPage(1)
              })}
            >
              {r.ip}
            </Typography.Link>
          )}
          {region && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {region}
            </Typography.Text>
          )}
        </div>
        {detail.length > 0 && (
          <div className="rp-audit-row__detail">
            <DetailParts t={t} parts={detail} />
          </div>
        )}
        <Typography.Text type="secondary" className="rp-audit-row__target">
          {t(`audit.t.${r.target_type}`, r.target_type)} {r.target_id}
        </Typography.Text>
        {at.local && (
          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
            {t('audit.yourTime', { at: at.local })}
          </Typography.Text>
        )}
        {at.legacy && (
          <Typography.Text type="secondary" style={{ fontSize: 11 }}>
            {t('audit.legacyTime')}
          </Typography.Text>
        )}
      </div>
    )
  }

  return (
    <Card
      title={t('audit.title')}
      extra={
        mobile ? undefined : (
          <Space wrap>
            {narrowFilters}
            {search}
          </Space>
        )
      }
      styles={{ body: { paddingTop: 12 } }}
    >
      {err && <Alert type="error" showIcon title={err} style={{ marginBottom: 12 }} />}
      <Typography.Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t('audit.intro')}
      </Typography.Paragraph>
      {/* A card header on a phone is barely wider than one of these controls, so they move into
          the body where they get the full width. */}
      {mobile && (
        <div className="rp-audit-filters">
          <div className="rp-audit-filters__top">
            {search}
            <Badge dot={Boolean(action || actor.trim() || ip.trim() || since)} offset={[-4, 4]}>
              <Button icon={<FilterOutlined />} onClick={() => setFiltersOpen((o) => !o)}>
                {t('audit.filters')}
              </Button>
            </Badge>
          </div>
          {filtersOpen && <div className="rp-audit-filters__grid">{narrowFilters}</div>}
        </div>
      )}
      {mobile ? (
        <Spin spinning={loading}>
          {items.length === 0 && !loading ? (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
          ) : (
            <div className="rp-audit-list">{items.map(rowCard)}</div>
          )}
          {pager}
        </Spin>
      ) : (
        <Table<AuditEntry>
          rowKey="id"
          size="small"
          loading={loading}
          dataSource={items}
          columns={columns}
          // `fixed` is load-bearing, not a preference. Left on auto, the browser sizes the detail
          // column to its content — so ONE row carrying a machine snapshot (a run window, a nested
          // input) widened the column to thousands of pixels, squeezed every other column to its
          // minimum and turned the page into a horizontal scroller showing a wall of JSON. That is
          // the shape the raw column was already failing in.
          tableLayout="fixed"
          // The fixed column widths add up to more than a small laptop or a portrait tablet has.
          // Scrolling the table is the honest answer; squeezing the columns is what produced the
          // one-character-per-line wrap this page used to show. The detail column takes what is left.
          scroll={{ x: 1200 }}
          pagination={{
            current: page,
            pageSize,
            total: data?.total ?? 0,
            showSizeChanger: false,
            onChange: setPage,
          }}
        />
      )}
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
          title={t('audit.proxyHintTitle')}
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

      {/* Everything the row carries, in the order an investigation reads it, and the payload
          exactly as stored — the sentence in the table is a rendering, and somebody chasing an
          incident needs the thing itself to paste into a ticket. */}
      <Modal
        open={row != null}
        title={t('audit.details')}
        footer={null}
        // A 640px dialog on a 390px phone is centred by antd and then clipped on both sides.
        width={mobile ? 'calc(100vw - 24px)' : 640}
        // `margin: 0 auto` because setting `top` replaces antd's own margin shorthand, and without
        // the auto the dialog sits against the left edge instead of centred.
        style={mobile ? { top: 16, maxWidth: 'calc(100vw - 24px)', margin: '0 auto' } : undefined}
        onCancel={() => setRow(null)}
      >
        {row && (
          // Labels above values on a phone: side by side, the label column eats a third of the
          // width and the address, the object and the payload each wrap to four lines.
          <Descriptions bordered size="small" column={1} layout={mobile ? 'vertical' : 'horizontal'}>
            <Descriptions.Item label={t('audit.at')}>{auditTime(row.at, data?.timezone ?? '').text}</Descriptions.Item>
            <Descriptions.Item label={t('audit.actor')}>
              {actorOf(row, t)}
              {row.actor_ou > 0 ? ` · ${ouNames[String(row.actor_ou)] ?? `OU ${row.actor_ou}`}` : ''}
            </Descriptions.Item>
            <Descriptions.Item label={t('audit.ipFilter')}>
              {row.ip ? `${row.ip}${formatRegion(row.geo) ? ` · ${formatRegion(row.geo)}` : ''}` : '—'}
            </Descriptions.Item>
            <Descriptions.Item label={t('audit.action')}>
              <Tag color={ACTION_COLOR[row.action]}>{t(`audit.a.${row.action}`, row.action)}</Tag>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {row.action}
              </Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label={t('audit.target')}>
              {t(`audit.t.${row.target_type}`, row.target_type)} {row.target_id}
            </Descriptions.Item>
            <Descriptions.Item label={t('audit.detail')}>
              {/* Unclamped here: this is the place the clamped column sends somebody who needs the
                  whole thing. Still bounded, so the dialog does not grow past the window. */}
              <DetailParts t={t} parts={auditDetail(row.action, row.detail, t, { ouNames })} full />
            </Descriptions.Item>
            <Descriptions.Item
              // Copy sits on the label rather than on the payload: antd puts the icon after its
              // child, and after a block of JSON that is a stray glyph on a line of its own.
              label={
                <Space size={4}>
                  {t('audit.raw')}
                  {row.detail ? <Typography.Text copyable={{ text: row.detail }} /> : null}
                </Space>
              }
            >
              {/* Bounded and scrollable: a grant change over a large OU can carry a long list, and
                  a dialog that grows past the window is one nobody can close. */}
              <pre style={{ margin: 0, maxHeight: 260, overflow: 'auto', whiteSpace: 'pre-wrap', wordBreak: 'break-all', fontSize: 12 }}>
                {row.detail || '—'}
              </pre>
            </Descriptions.Item>
          </Descriptions>
        )}
      </Modal>
    </Card>
  )
}
