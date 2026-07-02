import { useEffect, useState } from 'react'
import { Button, Card, Empty, Result, Space, Spin, Tabs, Tag, Typography } from 'antd'
import { ArrowLeftOutlined, DownloadOutlined, FilePdfOutlined } from '@ant-design/icons'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, qs, ApiError } from '../api/client'
import type { RunResp } from '../api/types'
import Markdown from '../components/Markdown'
import { exportReportPdf } from '../lib/exportPdf'

export default function RunPage() {
  const { t } = useTranslation()
  const { key = '' } = useParams()
  const [sp, setSp] = useSearchParams()
  const navigate = useNavigate()
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
            <Space>
              <Button icon={<DownloadOutlined />} href={`/report/${rep.rid}/md`}>
                {t('stock.exportMd')}
              </Button>
              <Button
                icon={<FilePdfOutlined />}
                onClick={() =>
                  exportReportPdf(rep.rid, {
                    title: rep.title,
                    date: rep.date,
                    source: rep.source,
                    html: rep.html,
                    md: rep.md,
                  })
                }
              >
                {t('stock.exportPdf')}
              </Button>
            </Space>
          )}
        </Space>

        <Card
          styles={{
            // Tabs bring their own baseline; drop the card-head border to avoid a double line.
            header: data.tabs.length > 1 ? { borderBottom: 'none' } : {},
          }}
          title={
            data.tabs.length > 1 ? (
              <Tabs
                activeKey={data.selRID}
                onChange={(rid) => setSp({ r: rid })}
                items={data.tabs.map((s) => ({ key: s.rid, label: s.label }))}
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
    </Spin>
  )
}
