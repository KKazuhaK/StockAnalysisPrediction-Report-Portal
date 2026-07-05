import { useEffect, useState, type CSSProperties } from 'react'
import { Button, Card, Space, Spin, Table, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import Markdown from '../../components/Markdown'
import { specToEndpoints, type ApiEndpoint, type ApiParam, type ApiError } from './openapiDoc'

const METHOD_COLORS: Record<string, string> = {
  GET: 'green',
  POST: 'blue',
  PUT: 'gold',
  PATCH: 'orange',
  DELETE: 'red',
}

const CODE_BLOCK: CSSProperties = {
  background: 'rgba(128,128,128,0.12)',
  padding: 12,
  borderRadius: 8,
  overflow: 'auto',
  fontSize: 12,
  lineHeight: 1.6,
  margin: '4px 0 0',
  whiteSpace: 'pre',
}

function EndpointCard({ e }: { e: ApiEndpoint }) {
  const { t } = useTranslation()
  return (
    <Card size="small">
      <Space direction="vertical" size={10} style={{ width: '100%' }}>
        <Space wrap align="center">
          <Tag color={METHOD_COLORS[e.method] || 'default'} style={{ fontFamily: 'monospace', fontWeight: 600, margin: 0 }}>
            {e.method}
          </Tag>
          <Typography.Text code copyable={{ text: e.path }} style={{ fontSize: 14 }}>
            {e.path}
          </Typography.Text>
          <Tag>{e.scope}</Tag>
        </Space>
        <Typography.Text type="secondary">{e.summary}</Typography.Text>

        {e.params.length > 0 && (
          <Table<ApiParam>
            size="small"
            pagination={false}
            scroll={{ x: 'max-content' }}
            rowKey={(p) => `${p.in}:${p.name}`}
            dataSource={e.params}
            columns={[
              { title: t('apidoc.param'), dataIndex: 'name', width: 130, render: (n: string) => <Typography.Text code>{n}</Typography.Text> },
              { title: t('apidoc.in'), dataIndex: 'in', width: 64 },
              { title: t('apidoc.type'), dataIndex: 'type', width: 108 },
              {
                title: t('apidoc.required'),
                dataIndex: 'required',
                width: 60,
                align: 'center',
                render: (v: boolean) => (v ? <Tag color="red">{t('apidoc.required')}</Tag> : <Typography.Text type="secondary">—</Typography.Text>),
              },
              { title: t('apidoc.desc'), dataIndex: 'desc' },
            ]}
          />
        )}

        <div>
          <Typography.Text strong style={{ fontSize: 12 }}>
            {t('apidoc.requestExample')}
          </Typography.Text>
          <pre style={CODE_BLOCK}>{e.requestExample}</pre>
        </div>
        <div>
          <Typography.Text strong style={{ fontSize: 12 }}>
            {t('apidoc.responseExample')}
          </Typography.Text>
          <pre style={CODE_BLOCK}>{e.responseExample}</pre>
        </div>

        {e.errors.length > 0 && (
          <Table<ApiError>
            size="small"
            pagination={false}
            scroll={{ x: 'max-content' }}
            rowKey={(er) => String(er.code)}
            dataSource={e.errors}
            columns={[
              { title: t('apidoc.statusCode'), dataIndex: 'code', width: 80, render: (c: number) => <Tag color="volcano">{c}</Tag> },
              { title: t('apidoc.when'), dataIndex: 'when' },
            ]}
          />
        )}

        {e.notes && (
          <Typography.Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
            {e.notes}
          </Typography.Paragraph>
        )}
      </Space>
    </Card>
  )
}

// Live reference for the /api/v1 machine API, rendered from the served openapi.json.
export default function ApiDocPage() {
  const { t } = useTranslation()
  const [doc, setDoc] = useState<{ conventions: string; endpoints: ApiEndpoint[] } | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    fetch('/api/openapi.json', { credentials: 'same-origin' })
      .then((r) => r.json())
      .then((spec) => setDoc(specToEndpoints(spec, window.location.origin)))
      .catch(() => setFailed(true))
  }, [])

  if (failed) return <Typography.Text type="danger">{t('apidoc.loadFailed')}</Typography.Text>
  if (!doc) return <Spin />

  return (
    <Space direction="vertical" size={12} style={{ width: '100%' }}>
      <Space wrap>
        <Tag color="geekblue">OpenAPI 3.1</Tag>
        <Button size="small" href="/api/openapi.json" target="_blank" rel="noreferrer">
          {t('apidoc.download')}
        </Button>
      </Space>
      <Card size="small" title={t('apidoc.conventions')}>
        <Markdown md={doc.conventions} />
      </Card>
      {doc.endpoints.map((e) => (
        <EndpointCard key={`${e.method} ${e.path}`} e={e} />
      ))}
    </Space>
  )
}
