import { useEffect, useState, type CSSProperties } from 'react'
import { Button, Card, Empty, Grid, Result, Segmented, Space, Spin, Tag, Typography } from 'antd'
import { ArrowLeftOutlined, ClockCircleOutlined, DownloadOutlined } from '@ant-design/icons'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, qs, ApiError } from '../api/client'
import type { StockResp } from '../api/types'
import Markdown from '../components/Markdown'
import ReaderControls from '../components/ReaderControls'
import TimelinePanel from '../components/TimelinePanel'
import { ExportPdfButton, ExportDayButton } from '../components/ExportButtons'
import { useReaderPrefs } from '../reader'
import { formatReportDateTime, isInstant } from '../lib/datetime'

export default function StockPage() {
  const { t } = useTranslation()
  const { symbol = '' } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const { fontSize, fontWeight, wide } = useReaderPrefs()
  const readerVars = { '--md-fs': `${fontSize}px`, '--md-fw': String(fontWeight) } as CSSProperties
  // Optimal reading width the centered column fluidly grows up to, then caps at (so it
  // fills small screens but never runs to unreadable line lengths on ultra-wide ones);
  // "wide" mode opts into a roomier cap for those who want to use the extra space.
  const layoutVars = { '--rp-doc-max': wide ? '1440px' : '1080px' } as CSSProperties
  const [data, setData] = useState<StockResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [notFound, setNotFound] = useState(false)

  const query = { date: sp.get('date') || '', kind: sp.get('kind') || '', r: sp.get('r') || '' }

  useEffect(() => {
    setLoading(true)
    setNotFound(false)
    api
      .get<StockResp>(`/api/stock/${encodeURIComponent(symbol)}${qs(query)}`)
      .then(setData)
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setNotFound(true)
      })
      .finally(() => setLoading(false))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [symbol, sp])

  if (notFound) {
    return (
      <Result
        status="404"
        title={symbol}
        subTitle={t('home.empty')}
        extra={
          <Button type="primary" onClick={() => navigate('/')}>
            {t('stock.back')}
          </Button>
        }
      />
    )
  }

  if (loading && !data) {
    return (
      <div style={{ padding: 80, textAlign: 'center' }}>
        <Spin size="large" />
      </div>
    )
  }
  if (!data) return null

  const setDate = (d: string) => setSp({ date: d })
  const setKind = (k: string) => setSp({ date: data.selDate, kind: k })
  const setRid = (rid: string) => setSp({ date: data.selDate, kind: data.selKind, r: rid })
  const rep = data.rep
  const compact = !Grid.useBreakpoint().md // phone / small tablet

  // Back + stock name/code + export. On desktop it sits inside the reading column (so the
  // export buttons line up with the report's right edge); on mobile it moves to the very
  // top — above the timeline — so navigation is the first thing you see.
  const headerBar = (
    <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
      <Space size={12} wrap>
        <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/')}>
          {t('stock.back')}
        </Button>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {data.name}{' '}
          <Typography.Text type="secondary" style={{ fontSize: 15 }}>
            {data.symbol}
          </Typography.Text>
        </Typography.Title>
        {rep && rep.name && rep.name !== data.name && (
          <Tag color="orange">
            {t('stock.asOf')}: {rep.name}
          </Tag>
        )}
      </Space>
      {rep && (
        <Space wrap>
          <Button icon={<DownloadOutlined />} href={`/report/${rep.rid}/md`}>
            {t('stock.exportMd')}
          </Button>
          <ExportPdfButton
            rid={rep.rid}
            report={{ title: rep.title, date: rep.date, source: rep.source, html: rep.html, md: rep.md }}
          />
          <ExportDayButton symbol={data.symbol} date={data.selDate} name={data.name} />
        </Space>
      )}
    </Space>
  )

  return (
    <Spin spinning={loading}>
      <div className={`rp-reader${wide ? ' rp-reader--wide' : ''}`} style={layoutVars}>
        {/* Mobile: back + stock name/code + export sit above the timeline. */}
        {compact && <div style={{ marginBottom: 12 }}>{headerBar}</div>}

        {/* Narrow / wide mode: the timeline is a horizontal strip on top; normal mode on a
            roomy screen floats it as a left rail (container query in index.css). */}
        <div className="rp-reader__strip">
          <Card size="small" title={t('stock.timeline')} styles={{ body: { paddingTop: 12 } }}>
            <TimelinePanel nodes={data.timeline} selected={data.selDate} onSelect={setDate} horizontal />
          </Card>
        </div>

        <div className="rp-reader__body">
          {/* Wide: the timeline is a fixed left column beside the reading column. */}
          <div className="rp-reader__rail">
            <Card size="small" title={t('stock.timeline')} styles={{ body: { paddingTop: 16 } }}>
              <TimelinePanel nodes={data.timeline} selected={data.selDate} onSelect={setDate} horizontal={false} />
            </Card>
          </div>

          <div className="rp-reader__doc">
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              {/* Desktop keeps the header inside the reading column. */}
              {!compact && headerBar}

              {data.kinds.length > 1 && (
                  <div style={{ overflowX: 'auto', overscrollBehaviorX: 'contain' }}>
                    <Segmented
                      value={data.selKind}
                      onChange={(v) => setKind(String(v))}
                      options={data.kinds.map((k) => ({ label: k, value: k }))}
                    />
                  </div>
                )}
                {data.subtabs.length > 1 && (
                  // Report-type strip: a horizontal-scroll Segmented (same pattern as the
                  // category strip above) so it swipes smoothly on mobile instead of
                  // dragging the whole page.
                  <div style={{ overflowX: 'auto', overscrollBehaviorX: 'contain' }}>
                    <Segmented
                      value={data.selRID}
                      onChange={(v) => setRid(String(v))}
                      options={data.subtabs.map((s) => ({ label: s.label, value: s.rid }))}
                    />
                  </div>
                )}
                <Card
                  className="rp-doc-card"
                  styles={{ body: { paddingTop: 8 } }}
                  title={rep?.title}
                  extra={rep ? <ReaderControls /> : undefined}
                  style={readerVars}
                >
                  {rep && isInstant(rep.time) && (
                    <Typography.Text
                      type="secondary"
                      title={formatReportDateTime(rep.time)}
                      style={{ fontSize: 12, display: 'block', marginBottom: 8 }}
                    >
                      <ClockCircleOutlined /> {formatReportDateTime(rep.time)}
                    </Typography.Text>
                  )}
                  {rep ? <Markdown md={rep.md} html={rep.html} /> : <Empty />}
                </Card>
              </Space>
            </div>
          </div>
        </div>
    </Spin>
  )
}
