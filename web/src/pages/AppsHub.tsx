import type { ReactNode } from 'react'
import { Card, Col, Empty, Row, Space, Typography, theme } from 'antd'
import { ThunderboltOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../auth'

// The apps hub: a grid of user-facing apps, each gated by a permission. Batch-run
// is the first; more (webhook-driven integrations, …) get added as cards here.
interface AppDef {
  key: string
  perm: string
  to: string
  icon: ReactNode
  titleKey: string
  descKey: string
}

const APPS: AppDef[] = [
  {
    key: 'batch',
    perm: 'run_batch',
    to: '/apps/batch',
    icon: <ThunderboltOutlined />,
    titleKey: 'nav.batch',
    descKey: 'apps.batchDesc',
  },
]

export default function AppsHub() {
  const { t } = useTranslation()
  const { can } = useAuth()
  const navigate = useNavigate()
  const { token } = theme.useToken()
  const available = APPS.filter((a) => can(a.perm))

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        {t('nav.apps')}
      </Typography.Title>
      {available.length === 0 ? (
        <Empty description={t('apps.empty')} />
      ) : (
        <Row gutter={[16, 16]}>
          {available.map((a) => (
            <Col key={a.key} xs={24} sm={12} lg={8}>
              <Card hoverable onClick={() => navigate(a.to)} style={{ height: '100%' }}>
                <Space align="start" size={16}>
                  <span style={{ fontSize: 28, color: token.colorPrimary, lineHeight: 1 }}>{a.icon}</span>
                  <div>
                    <Typography.Text strong style={{ fontSize: 16 }}>
                      {t(a.titleKey)}
                    </Typography.Text>
                    <Typography.Paragraph type="secondary" style={{ margin: '4px 0 0' }}>
                      {t(a.descKey)}
                    </Typography.Paragraph>
                  </div>
                </Space>
              </Card>
            </Col>
          ))}
        </Row>
      )}
    </Space>
  )
}
