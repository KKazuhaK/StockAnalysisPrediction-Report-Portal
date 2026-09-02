import { Space, Tag, Typography, theme } from 'antd'
import { useTranslation } from 'react-i18next'
import type { SectionDiff } from '../api/types'

// One section of a Markdown diff, rendered.
//
// Extracted from CompareModal so the report comparison and the edit history draw the same picture
// rather than two pictures of the same server payload. Both receive `SectionDiff` from the same
// diffMarkdown, so there was nothing to generalise — the only thing CompareModal held onto that the
// history could not use was the report id it fetches by, which is why this is an extraction rather
// than a modal with more props.

// A section's colour carries its status, so the shape of the change is readable before any text is.
export function StatusTag({ status }: { status: string }) {
  const { t } = useTranslation()
  switch (status) {
    case 'added':
      return <Tag color="green">{t('compare.added')}</Tag>
    case 'removed':
      return <Tag color="red">{t('compare.removed')}</Tag>
    case 'changed':
      return <Tag color="gold">{t('compare.changed')}</Tag>
    default:
      return null
  }
}

export function SectionBlock({ sec }: { sec: SectionDiff }) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const tint = (op: string) =>
    op === '+' ? token.colorSuccessBg : op === '-' ? token.colorErrorBg : 'transparent'

  return (
    <div style={{ marginBottom: 14 }}>
      <Space size={8} align="center" style={{ marginBottom: 4 }}>
        <Typography.Text strong>{sec.heading || t('compare.preamble')}</Typography.Text>
        <StatusTag status={sec.status} />
      </Space>
      {(sec.lines?.length ?? 0) > 0 && (
        <div
          style={{
            border: `1px solid ${token.colorBorderSecondary}`,
            borderRadius: token.borderRadius,
            overflowX: 'auto',
            fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
            fontSize: 12,
          }}
        >
          {sec.lines?.map((l, i) => (
            <div key={i} style={{ background: tint(l.op), padding: '1px 8px', whiteSpace: 'pre-wrap' }}>
              <span style={{ opacity: 0.45, userSelect: 'none' }}>{l.op === ' ' ? ' ' : l.op} </span>
              {l.text}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
