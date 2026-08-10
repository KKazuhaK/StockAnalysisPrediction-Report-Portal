import type { ReactNode } from 'react'
import { Button, Result, Spin, Typography } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'

// What a settings page shows while its own configuration is still on the wire.
//
// The alternative — and what several pages did — is to render the form immediately with the
// values its useState calls happen to start at. On a fast link that reads as a flicker; on a
// slow one, or a failed request, it is a page telling an admin that the queue runs one job at
// a time and mail is switched off, in a form whose Save button will happily write exactly that
// back. A default is not a value the server sent, and must never be presented as one.
//
// So: a spinner until the answer is in, and if it never comes, say so and offer the retry —
// never fall through to the defaults.
export default function LoadGate({
  loading,
  error,
  onRetry,
  children,
  minHeight = '40vh',
}: {
  loading: boolean
  error?: string
  onRetry?: () => void
  children: ReactNode
  minHeight?: number | string
}) {
  const { t } = useTranslation()
  if (error) {
    return (
      <Result
        status="warning"
        title={t('common.loadFailed')}
        subTitle={error}
        extra={
          onRetry ? (
            <Button icon={<ReloadOutlined />} onClick={onRetry}>
              {t('common.retry')}
            </Button>
          ) : undefined
        }
      />
    )
  }
  if (loading) {
    // Spinner + label as two siblings rather than antd's `tip`, which only renders in Spin's
    // nest mode and would need a dummy child to wrap.
    return (
      <div style={{ display: 'grid', alignContent: 'center', justifyItems: 'center', gap: 12, minHeight }}>
        <Spin size="large" />
        <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
      </div>
    )
  }
  return <>{children}</>
}
