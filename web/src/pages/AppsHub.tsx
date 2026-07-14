import { useEffect, useState, type ReactNode } from 'react'
import { Card, Col, Empty, Row, Space, Tag, Typography, theme } from 'antd'
import { AppstoreOutlined, ClockCircleOutlined, PlayCircleOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { useAuth } from '../auth'
import type { AppsResp, AppSummary } from '../api/types'

// The apps hub: a grid of user-facing apps. Batch-run is a compiled-in card (gated
// by a permission); installed iframe apps (ADR 0003) are downloaded at runtime and
// appear here for every user. Empty by default — the "app center" fills as you
// install apps.
interface BuiltinApp {
  key: string
  perm: string
  to: string
  icon: ReactNode
  titleKey: string
  descKey: string
}

const BUILTINS: BuiltinApp[] = [
  {
    key: 'batch',
    perm: 'run_batch',
    to: '/apps/batch',
    icon: <PlayCircleOutlined />,
    titleKey: 'nav.batch',
    descKey: 'apps.batchDesc',
  },
  {
    key: 'recurring',
    perm: 'run_batch',
    to: '/apps/recurring',
    icon: <ClockCircleOutlined />,
    titleKey: 'nav.recurring',
    descKey: 'apps.recurringDesc',
  },
]

function AppCard({ icon, title, desc, tag, onClick }: { icon: ReactNode; title: string; desc?: string; tag?: string; onClick: () => void }) {
  const { token } = theme.useToken()
  return (
    <Col xs={24} sm={12} lg={8}>
      <Card hoverable onClick={onClick} style={{ height: '100%' }}>
        <Space align="start" size={16}>
          <span style={{ fontSize: 28, color: token.colorPrimary, lineHeight: 1 }}>{icon}</span>
          <div>
            <Space size={8} align="center">
              <Typography.Text strong style={{ fontSize: 16 }}>
                {title}
              </Typography.Text>
              {tag && <Tag color="blue">{tag}</Tag>}
            </Space>
            {desc && (
              <Typography.Paragraph type="secondary" style={{ margin: '4px 0 0' }}>
                {desc}
              </Typography.Paragraph>
            )}
          </div>
        </Space>
      </Card>
    </Col>
  )
}

export default function AppsHub() {
  const { t } = useTranslation()
  const { can } = useAuth()
  const navigate = useNavigate()
  const [apps, setApps] = useState<AppSummary[]>([])

  useEffect(() => {
    api
      .get<AppsResp>('/api/apps')
      .then((r) => setApps(r.apps || []))
      .catch(() => setApps([]))
  }, [])

  const builtins = BUILTINS.filter((a) => can(a.perm))
  const isEmpty = builtins.length === 0 && apps.length === 0

  return (
    <Space direction="vertical" size={16} style={{ width: '100%' }}>
      <Typography.Title level={4} style={{ margin: 0 }}>
        {t('nav.apps')}
      </Typography.Title>
      {isEmpty ? (
        <Empty description={t('apps.empty')} />
      ) : (
        <Row gutter={[16, 16]}>
          {builtins.map((a) => (
            <AppCard
              key={a.key}
              icon={a.icon}
              title={t(a.titleKey)}
              desc={t(a.descKey)}
              tag={t('apps.builtin')}
              onClick={() => navigate(a.to)}
            />
          ))}
          {apps.map((a) => (
            <AppCard
              key={a.id}
              icon={a.icon ? <span style={{ fontSize: 28, lineHeight: 1 }}>{a.icon}</span> : <AppstoreOutlined />}
              title={a.name}
              desc={a.version ? `v${a.version}` : undefined}
              onClick={() => navigate(`/apps/x/${a.id}`)}
            />
          ))}
        </Row>
      )}
    </Space>
  )
}
