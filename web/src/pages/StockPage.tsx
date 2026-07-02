import { useEffect, useState } from 'react'
import { Button, Card, Col, Empty, Result, Row, Segmented, Space, Spin, Tabs, Tag, Timeline, Typography, theme } from 'antd'
import { ArrowLeftOutlined, DownloadOutlined, FilePdfOutlined } from '@ant-design/icons'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, qs, ApiError } from '../api/client'
import type { StockResp } from '../api/types'
import Markdown from '../components/Markdown'

export default function StockPage() {
  const { t } = useTranslation()
  const { symbol = '' } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
  const { token } = theme.useToken()
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
            <Space>
              <Button icon={<DownloadOutlined />} href={`/report/${rep.rid}/md`}>
                {t('stock.exportMd')}
              </Button>
              <Button icon={<FilePdfOutlined />} href={`/report/${rep.rid}/pdf`} target="_blank" rel="noreferrer">
                {t('stock.exportPdf')}
              </Button>
            </Space>
          )}
        </Space>

        <Row gutter={20}>
          {/* Timeline */}
          <Col xs={24} md={6}>
            <Card size="small" title={t('stock.timeline')} styles={{ body: { paddingTop: 16 } }}>
              <Timeline
                items={data.timeline.map((n) => ({
                  color: n.date === data.selDate ? token.colorPrimary : 'gray',
                  children: (
                    <a
                      onClick={() => setDate(n.date)}
                      style={{
                        fontWeight: n.date === data.selDate ? 600 : 400,
                        color: n.date === data.selDate ? token.colorPrimary : token.colorText,
                      }}
                    >
                      {n.date}
                      <Typography.Text type="secondary" style={{ marginLeft: 6, fontSize: 12 }}>
                        {n.n}
                      </Typography.Text>
                    </a>
                  ),
                }))}
              />
            </Card>
          </Col>

          {/* Main content area */}
          <Col xs={24} md={18}>
            <Space direction="vertical" size={12} style={{ width: '100%' }}>
              {data.kinds.length > 1 && (
                <Segmented
                  value={data.selKind}
                  onChange={(v) => setKind(String(v))}
                  options={data.kinds.map((k) => ({ label: k, value: k }))}
                />
              )}
              <Card
                styles={{ body: { paddingTop: 8 } }}
                tabList={undefined}
                title={
                  data.subtabs.length > 1 ? (
                    <Tabs
                      activeKey={data.selRID}
                      onChange={setRid}
                      items={data.subtabs.map((s) => ({ key: s.rid, label: s.label }))}
                      style={{ marginBottom: -16 }}
                    />
                  ) : (
                    rep?.title
                  )
                }
              >
                {rep ? <Markdown md={rep.md} html={rep.html} /> : <Empty />}
              </Card>
            </Space>
          </Col>
        </Row>
      </Space>
    </Spin>
  )
}
