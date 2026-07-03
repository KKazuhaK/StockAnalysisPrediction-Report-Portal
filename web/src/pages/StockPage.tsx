import { useEffect, useState, type CSSProperties } from 'react'
import { Button, Card, Col, Empty, Grid, Result, Row, Segmented, Space, Spin, Tag, Typography } from 'antd'
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
  const screens = Grid.useBreakpoint()
  const isMobile = !screens.md
  const { fontSize, fontWeight, wide } = useReaderPrefs()
  const readerVars = { '--md-fs': `${fontSize}px`, '--md-fw': String(fontWeight) } as CSSProperties
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

  return (
    <Spin spinning={loading}>
      <Space direction="vertical" size={16} style={{ width: '100%' }}>
        {/* Header */}
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
              <ReaderControls />
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

        <Row gutter={[20, 16]}>
          {/* Timeline — vertical scroll box on desktop, horizontal chip strip on mobile.
              It only holds a date + count, so it narrows on wider screens (and further
              in wide mode) to hand that width to the reading column. Kept a touch wider
              at the md breakpoint (768–992) so the date never wraps on small laptops. */}
          <Col xs={24} md={wide ? 5 : 6} lg={wide ? 4 : 5}>
            <Card size="small" title={t('stock.timeline')} styles={{ body: { paddingTop: 16 } }}>
              <TimelinePanel nodes={data.timeline} selected={data.selDate} onSelect={setDate} horizontal={isMobile} />
            </Card>
          </Col>

          {/* Main content area */}
          <Col xs={24} md={wide ? 19 : 18} lg={wide ? 20 : 19}>
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
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
              <Card styles={{ body: { paddingTop: 8 } }} title={rep?.title} style={readerVars}>
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
          </Col>
        </Row>
      </Space>
    </Spin>
  )
}
