import { useEffect, useState, type CSSProperties } from 'react'
import { Button, Card, Empty, Result, Segmented, Space, Spin, Tag, Typography } from 'antd'
import { ArrowLeftOutlined, DownloadOutlined } from '@ant-design/icons'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, qs, ApiError } from '../api/client'
import type { RunResp } from '../api/types'
import Markdown from '../components/Markdown'
import ReaderControls from '../components/ReaderControls'
import { ExportPdfButton } from '../components/ExportButtons'
import { useReaderPrefs } from '../reader'

export default function RunPage() {
  const { t } = useTranslation()
  const { key = '' } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const { fontSize, fontWeight, wide } = useReaderPrefs()
  const readerVars = { '--md-fs': `${fontSize}px`, '--md-fw': String(fontWeight) } as CSSProperties
  // Same optimal reading width as the stock page: fill up to the cap, then center.
  const docMax = wide ? 1440 : 1080
  const [data, setData] = useState<RunResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [notFound, setNotFound] = useState(false)

  const r = sp.get('r') || ''

  useEffect(() => {
    setLoading(true)
    setNotFound(false)
    api
      .get<RunResp>(`/api/run/${encodeURIComponent(key)}${qs({ r })}`)
      .then(setData)
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setNotFound(true)
      })
      .finally(() => setLoading(false))
  }, [key, r])

  if (notFound) {
    return (
      <Result
        status="404"
        title={t('home.empty')}
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
  const rep = data.rep

  return (
    <Spin spinning={loading}>
      {/* Same reader layout as the stock page (an empty rail where its timeline would be),
          so timeline and non-timeline reports get an identical, wide-mode-aware column. */}
      <div className="rp-reader" style={{ '--rp-doc-max': `${docMax}px` } as CSSProperties}>
        <div className="rp-reader__body">
          <div className="rp-reader__rail" aria-hidden />
          <div className="rp-reader__doc">
            <Space direction="vertical" size={16} style={{ width: '100%' }}>
        <Space style={{ justifyContent: 'space-between', width: '100%' }} wrap>
          <Space size={12} wrap>
            <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/')}>
              {t('stock.back')}
            </Button>
            <Typography.Title level={4} style={{ margin: 0 }}>
              {data.name || data.symbol}{' '}
              <Typography.Text type="secondary" style={{ fontSize: 15 }}>
                {data.date}
              </Typography.Text>
            </Typography.Title>
            {rep && rep.name && data.name && rep.name !== data.name && (
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
            </Space>
          )}
        </Space>

        {data.tabs.length > 1 && (
          // Report-type strip: a horizontal-scroll Segmented so it swipes smoothly on
          // mobile instead of dragging the whole page.
          <div style={{ overflowX: 'auto', overscrollBehaviorX: 'contain' }}>
            <Segmented
              value={data.selRID}
              onChange={(v) => setSp({ r: String(v) })}
              options={data.tabs.map((s) => ({ label: s.label, value: s.rid }))}
            />
          </div>
        )}
              <Card title={rep?.title} extra={rep ? <ReaderControls /> : undefined} style={readerVars}>
                {rep ? <Markdown md={rep.md} html={rep.html} /> : <Empty />}
              </Card>
            </Space>
          </div>
        </div>
      </div>
    </Spin>
  )
}
