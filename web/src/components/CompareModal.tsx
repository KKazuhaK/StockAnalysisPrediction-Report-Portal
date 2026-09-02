import { useCallback, useEffect, useMemo, useState } from 'react'
import { Alert, Empty, Modal, Select, Space, Spin, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import type { ComparableReport, ReportDiff } from '../api/types'
import { SectionBlock } from './DiffSections'

// Comparing this report with another edition of the same analysis.
//
// The pipeline regenerates the same report for the same symbol over and over, so what changed IS
// the news. The diff is computed server-side against the documents' own headings, which is what
// makes it work across every report type without knowing anything about their formats.

export default function CompareModal({
  reportId,
  open,
  onClose,
}: {
  reportId: number
  open: boolean
  onClose: () => void
}) {
  const { t } = useTranslation()
  // null until the comparable-editions list has actually come back: "there is no earlier edition
  // to compare with" is the reason this dialog would have nothing to do, and it must be the
  // server's answer rather than the state's starting value.
  const [candidates, setCandidates] = useState<ComparableReport[] | null>(null)
  const [against, setAgainst] = useState<number>()
  const [diff, setDiff] = useState<ReportDiff | null>(null)
  const [loading, setLoading] = useState(false)
  const [err, setErr] = useState('')
  // Unchanged sections are collapsed by default: on a long report they are most of it, and the
  // point of opening this is the part that moved.
  const [showSame, setShowSame] = useState(false)

  useEffect(() => {
    if (!open) return
    setErr('')
    api
      .get<{ items: ComparableReport[] }>(`/api/reports/comparable?id=${reportId}`)
      .then((r) => {
        const items = r.items || []
        setCandidates(items)
        // The previous edition is the comparison people want; the rest are one click away.
        setAgainst(items[0]?.id)
      })
      .catch((e) => setErr(errText(e, t)))
    // `t` is deliberately not a dependency: it is used only to word an error, and re-running this
    // effect for a new `t` identity re-fetches the list — which, since the answer replaces state,
    // re-renders and asks again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, reportId])

  const load = useCallback(() => {
    if (!against) return
    setLoading(true)
    setErr('')
    api
      .get<ReportDiff>(`/api/reports/diff?a=${against}&b=${reportId}`)
      .then(setDiff)
      .catch((e) => setErr(errText(e, t)))
      .finally(() => setLoading(false))
    // `t` omitted for the same reason as above — it only words the error.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [against, reportId])
  useEffect(load, [load])

  const shown = useMemo(
    () => (diff?.sections ?? []).filter((s) => showSame || s.status !== 'same'),
    [diff, showSame],
  )

  return (
    <Modal open={open} onCancel={onClose} footer={null} width={860} title={t('compare.title')} destroyOnHidden>
      <Space direction="vertical" size={12} style={{ width: '100%' }}>
        {candidates == null && !err ? (
          <div style={{ display: 'grid', justifyItems: 'center', gap: 12, padding: 24 }}>
            <Spin />
            <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
          </div>
        ) : (candidates?.length ?? 0) === 0 && !err ? (
          <Empty description={t('compare.noneToCompare')} />
        ) : (
          <Space wrap>
            <Typography.Text type="secondary">{t('compare.against')}</Typography.Text>
            <Select
              style={{ minWidth: 320 }}
              value={against}
              onChange={setAgainst}
              options={(candidates ?? []).map((c) => ({
                value: c.id,
                label: `${c.date}${c.version ? ` · ${c.version}` : ''} · ${c.title}`,
              }))}
            />
            <a onClick={() => setShowSame((v) => !v)}>
              {showSame ? t('compare.hideUnchanged') : t('compare.showUnchanged')}
            </a>
          </Space>
        )}

        {err && <Alert type="error" showIcon message={err} />}

        {loading ? (
          <Spin />
        ) : diff ? (
          <>
            <Typography.Text type="secondary">
              {diff.changed === 0
                ? t('compare.identical')
                : t('compare.changedCount', { n: diff.changed })}
            </Typography.Text>
            {/* Which direction is which: the older edition on the left of the sentence, so a
                removed line reads as "was in the old one". */}
            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block' }}>
              {t('compare.direction', { a: `${diff.a.date} ${diff.a.title}`, b: `${diff.b.date} ${diff.b.title}` })}
            </Typography.Text>
            <div>
              {shown.map((s, i) => (
                <SectionBlock key={`${s.heading}-${i}`} sec={s} />
              ))}
              {shown.length === 0 && <Empty description={t('compare.identical')} />}
            </div>
          </>
        ) : null}
      </Space>
    </Modal>
  )
}
