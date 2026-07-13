import { List, Modal, Space, Typography } from 'antd'
import { EditOutlined, EyeOutlined, KeyOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'

// The install-time permission prompt (ADR 0003 phase 2): before an app is installed,
// the admin sees exactly which API scopes it will be granted and confirms. The
// server-side token scope is the authoritative gate; this is the human check.
interface Props {
  open: boolean
  appName?: string
  scopes: string[]
  confirmLoading?: boolean
  onConfirm: () => void
  onCancel: () => void
}

// Human-readable copy per known scope; unknown scopes fall back to their raw name.
const SCOPE_LABEL: Record<string, string> = { query: 'apps.scopeQuery', ingest: 'apps.scopeIngest' }

function scopeIcon(scope: string) {
  if (scope === 'ingest') return <EditOutlined />
  if (scope === 'query') return <EyeOutlined />
  return <KeyOutlined />
}

export default function ScopePermissionModal({ open, appName, scopes, confirmLoading, onConfirm, onCancel }: Props) {
  const { t } = useTranslation()
  return (
    <Modal
      open={open}
      title={t('apps.permTitle')}
      okText={t('apps.permGrant')}
      cancelText={t('common.cancel')}
      confirmLoading={confirmLoading}
      onOk={onConfirm}
      onCancel={onCancel}
      destroyOnHidden
    >
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text>{t('apps.permIntro', { name: appName })}</Typography.Text>
        {scopes.length === 0 ? (
          <Typography.Text type="secondary">{t('apps.permNone')}</Typography.Text>
        ) : (
          <List
            size="small"
            bordered
            dataSource={scopes}
            renderItem={(s) => (
              <List.Item>
                <Space>
                  {scopeIcon(s)}
                  <span>{SCOPE_LABEL[s] ? t(SCOPE_LABEL[s]) : s}</span>
                </Space>
              </List.Item>
            )}
          />
        )}
      </Space>
    </Modal>
  )
}
