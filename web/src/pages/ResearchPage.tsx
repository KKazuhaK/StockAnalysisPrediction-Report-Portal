import { useEffect, useMemo, useState } from 'react'
import { Card, Empty, Input, List, Pagination, Space, Spin, Tag, Typography } from 'antd'
import { FileSearchOutlined } from '@ant-design/icons'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { api, qs } from '../api/client'
import type { ResearchResp } from '../api/types'

// 深度研究: free-form topic / Q&A reports that aren't tied to a single ticker.
// A flat, full-text-searchable, paginated list — parallel to the per-stock browse.
export default function ResearchPage() {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [sp, setSp] = useSearchParams()
  const [data, setData] = useState<ResearchResp | null>(null)
  const [loading, setLoading] = useState(true)

  const params = useMemo(
    () => ({ q: sp.get('q') || '', size: sp.get('size') || '30', page: sp.get('page') || '1' }),
    [sp],
  )

  useEffect(() => {
    setLoading(true)
    api
      .get<ResearchResp>(`/api/research${qs(params)}`)
      .then(setData)
      .finally(() => setLoading(false))
  }, [params])

  const onSearch = (v: string) =>
    setSp(v.trim() ? { q: v.trim(), page: '1', size: params.size } : { size: params.size })

  return (
    <Space direction="vertical" size={20} style={{ width: '100%' }}>
      <div style={{ textAlign: 'center', paddingTop: 8 }}>
        <Typography.Title level={3} style={{ display: 'inline-flex', alignItems: 'center', gap: 10, marginBottom: 8 }}>
          <FileSearchOutlined /> {t('nav.research')}
        </Typography.Title>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 16 }}>
          {t('research.subtitle')}
        </Typography.Paragraph>
        <div style={{ maxWidth: 640, margin: '0 auto' }}>
          <Input.Search
            size="large"
            allowClear
            defaultValue={params.q}
            placeholder={t('research.searchPlaceholder')}
            onSearch={onSearch}
          />
        </div>
      </div>

      <Spin spinning={loading}>
        {data && data.items.length === 0 ? (
          <Empty description={t('home.empty')} style={{ padding: '60px 0' }} />
        ) : (
          <Card styles={{ body: { padding: '4px 8px' } }}>
            <List
              itemLayout="horizontal"
              dataSource={data?.items || []}
              renderItem={(it) => (
                <List.Item
                  onClick={() => navigate(`/run/${encodeURIComponent(it.rid)}`)}
                  style={{ cursor: 'pointer', padding: '12px 12px' }}
                >
                  <List.Item.Meta
                    title={<Typography.Text strong>{it.title}</Typography.Text>}
                    description={
                      <Space size={8} wrap>
                        {it.rtype && <Tag>{it.rtype}</Tag>}
                        <Typography.Text type="secondary">{it.date}</Typography.Text>
                        {it.source && <Typography.Text type="secondary">· {it.source}</Typography.Text>}
                      </Space>
                    }
                  />
                </List.Item>
              )}
            />
          </Card>
        )}
      </Spin>

      {!!data && data.total > 0 && (
        <div style={{ textAlign: 'center' }}>
          <Pagination
            current={data.page}
            pageSize={Number(params.size)}
            total={data.total}
            showSizeChanger
            pageSizeOptions={['15', '30', '50']}
            onChange={(p, s) => setSp({ ...Object.fromEntries(sp), page: String(p), size: String(s) })}
            showTotal={(tt) => `${tt} ${t('home.reports')}`}
          />
        </div>
      )}
    </Space>
  )
}
