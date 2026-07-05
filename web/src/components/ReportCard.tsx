import { Card, Space, Tag, Typography } from 'antd'
import { CalendarOutlined, FileTextOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import type { Group } from '../api/types'
import { formatReportDateTime, isInstant } from '../lib/datetime'

export default function ReportCard({ g, kindColors }: { g: Group; kindColors?: Record<string, string> }) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const isNew = g.src === 'new'

  const open = () => {
    if (isNew && g.symbol) navigate(`/stock/${encodeURIComponent(g.symbol)}?date=${encodeURIComponent(g.date)}`)
    else navigate(`/run/${encodeURIComponent(g.key)}`)
  }

  // Prefer the (as-of) company name, then the code; for thematic reports with
  // neither, show the original document title instead of a bare "报告".
  const displayName = g.name || g.symbol || g.title || t('home.reports')

  return (
    <Card hoverable size="small" onClick={open} styles={{ body: { padding: 16 } }} style={{ height: '100%' }}>
      <Space direction="vertical" size={10} style={{ width: '100%' }}>
        <Space style={{ justifyContent: 'space-between', width: '100%' }} align="start">
          <div style={{ minWidth: 0, flex: 1 }}>
            <Typography.Paragraph
              strong
              style={{ fontSize: 16, marginBottom: 0 }}
              ellipsis={{ rows: 2, tooltip: displayName }}
            >
              {displayName}
            </Typography.Paragraph>
            {g.curName && g.curName !== g.name && (
              <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block' }}>
                {t('card.now')}: {g.curName}
              </Typography.Text>
            )}
          </div>
          {g.symbol && (
            <Typography.Text
              style={{ fontSize: 15, fontWeight: 500, fontVariantNumeric: 'tabular-nums', whiteSpace: 'nowrap' }}
            >
              {g.symbol}
            </Typography.Text>
          )}
        </Space>

        <Space size={[6, 6]} wrap>
          {(g.kinds?.length ? g.kinds : [g.kind]).filter(Boolean).map((k) => (
            <Tag key={k} color={kindColors?.[k] || 'default'} style={{ marginInlineEnd: 0 }}>
              {k}
            </Tag>
          ))}
          {!isNew && <Tag>{t('src.old')}</Tag>}
        </Space>

        <Space style={{ justifyContent: 'space-between', width: '100%' }}>
          <Typography.Text type="secondary" style={{ fontSize: 13 }}>
            <CalendarOutlined /> {isInstant(g.time) ? formatReportDateTime(g.time) : g.date}
          </Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 13 }}>
            <FileTextOutlined /> {g.n} {t('card.reports')}
          </Typography.Text>
        </Space>
      </Space>
    </Card>
  )
}
