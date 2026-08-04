import { useState } from 'react'
import { Alert, Checkbox, Modal, Space, Tag, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import type { DifyRefreshResult } from '../../api/types'

/**
 * What a pull from Dify would change, before it changes anything.
 *
 * A target's parameter list is how every future run of it is submitted, so this gets the same
 * preview-then-confirm treatment as a destructive cleanup rather than being applied on a click.
 *
 * The target's NAME is never in the payload — Dify's name is shown beside the local one purely so
 * an admin can tell they are looking at the right workflow. Same for the report subtype and the
 * stock-code column: local decisions the upstream has no say in.
 */

/** applicable = there is something to write. A failed probe has nothing; an unchanged one has
 *  nothing worth writing; a workflow whose /parameters did not answer has an empty list, and
 *  writing that would wipe whatever the admin has. */
export function applicable(r: DifyRefreshResult): boolean {
  return !r.error && !r.inputs_error && !!r.changed && (r.inputs?.length ?? 0) > 0
}

export default function DifyRefreshModal({
  open,
  results,
  onCancel,
  onApply,
}: {
  open: boolean
  results: DifyRefreshResult[]
  onCancel: () => void
  onApply: (picked: DifyRefreshResult[]) => Promise<void>
}) {
  const { t } = useTranslation()
  // Undefined until the admin touches a row: everything applicable starts selected, which is the
  // answer for the common case (a workflow gained a field) without hiding the ones that did not.
  const [off, setOff] = useState<Record<number, boolean>>({})
  const [busy, setBusy] = useState(false)
  const picked = results.filter((r) => applicable(r) && !off[r.id])
  const changedCount = results.filter(applicable).length

  const apply = async () => {
    setBusy(true)
    try {
      await onApply(picked)
      setOff({})
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      title={t('batch.refresh.title')}
      open={open}
      onCancel={onCancel}
      onOk={apply}
      confirmLoading={busy}
      okButtonProps={{ disabled: picked.length === 0 }}
      okText={t('batch.refresh.apply', { n: picked.length })}
      cancelText={t('common.cancel')}
      width={640}
    >
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        {changedCount === 0 && <Alert type="success" showIcon message={t('batch.refresh.noChanges')} />}
        {results.map((r) => (
          <Row key={r.id} r={r} disabled={!applicable(r)} checked={applicable(r) && !off[r.id]} onToggle={(v) => setOff({ ...off, [r.id]: !v })} />
        ))}
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('batch.refresh.scopeHint')}
        </Typography.Text>
      </Space>
    </Modal>
  )
}

function Row({
  r,
  checked,
  disabled,
  onToggle,
}: {
  r: DifyRefreshResult
  checked: boolean
  disabled: boolean
  onToggle: (v: boolean) => void
}) {
  const { t } = useTranslation()
  return (
    <div style={{ display: 'flex', gap: 10, alignItems: 'flex-start' }}>
      <Checkbox checked={checked} disabled={disabled} onChange={(e) => onToggle(e.target.checked)} style={{ marginTop: 2 }} />
      <div style={{ flex: 1, minWidth: 0 }}>
        <Space size={6} wrap>
          <Typography.Text strong>{r.local_name}</Typography.Text>
          {/* Shown, never written. An admin renames a target for their own reasons; the point of
              showing Dify's name is to confirm the key still points where they think it does. */}
          {r.name_differs && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('batch.refresh.remoteName', { name: r.remote_name })}
            </Typography.Text>
          )}
        </Space>
        <div style={{ marginTop: 4 }}>
          {r.error ? (
            <Typography.Text type="danger" style={{ fontSize: 12 }}>
              {t('batch.refresh.probeFailed', { error: r.error })}
            </Typography.Text>
          ) : r.inputs_error ? (
            <Typography.Text type="warning" style={{ fontSize: 12 }}>
              {t('batch.refresh.inputsUnavailable')}
            </Typography.Text>
          ) : !r.changed ? (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('batch.refresh.unchanged')}
            </Typography.Text>
          ) : (
            <Space size={4} wrap>
              {(r.added ?? []).map((v) => (
                <Tag key={'a' + v} color="green">
                  + <code>{v}</code>
                </Tag>
              ))}
              {(r.removed ?? []).map((v) => (
                <Tag key={'r' + v} color="red">
                  − <code>{v}</code>
                </Tag>
              ))}
              {(r.required_changed ?? []).map((v) => (
                <Tag key={'q' + v} color="orange">
                  <code>{v}</code> {t('batch.refresh.requiredChanged')}
                </Tag>
              ))}
              {r.reordered && <Tag>{t('batch.refresh.reordered')}</Tag>}
              {r.remote_mode && r.remote_mode !== r.local_mode && (
                <Tag color="purple">{t('batch.refresh.modeChanged', { from: r.local_mode || '—', to: r.remote_mode })}</Tag>
              )}
            </Space>
          )}
        </div>
        {/* Losing this one does not break a run — it quietly stops same-day reuse, so the portal
            pays to regenerate reports it already has. Worth more than a grey tag. */}
        {r.symbol_input_lost && (
          <Alert
            type="warning"
            showIcon
            style={{ marginTop: 6 }}
            message={t('batch.refresh.symbolLost', { name: r.symbol_input_lost })}
          />
        )}
      </div>
    </div>
  )
}
