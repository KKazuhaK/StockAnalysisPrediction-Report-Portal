import { useEffect, useState } from 'react'
import { Segmented, Tooltip } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import { formatReportDateTime } from '../lib/datetime'

// The reading page's version switcher (ADR 0024). One analysis is published in several written
// forms — an internal one carrying the scoring table, an external one carrying the conclusion — and
// each is produced by its own run.
//
// It renders nothing unless the reader may open more than one form. A single-version report is the
// overwhelmingly common case, and a switcher with one option is noise that also implies the others
// exist. The server decides what is listed: forms that were never generated are absent, and forms
// the reader is not granted never appear.

export interface ReportVersionInfo {
  id: number
  version: string
  label: string
  title: string
  time?: string
  current?: boolean
}

export default function VersionSwitcher({
  reportId,
  onPick,
}: {
  reportId: number
  onPick: (id: number) => void
}) {
  const { t } = useTranslation()
  const [versions, setVersions] = useState<ReportVersionInfo[]>([])

  useEffect(() => {
    let live = true
    api
      .get<{ versions: ReportVersionInfo[] }>(`/api/report/${reportId}/versions`)
      .then((r) => live && setVersions(r.versions ?? []))
      .catch(() => live && setVersions([]))
    return () => {
      live = false
    }
  }, [reportId])

  if (versions.length < 2) return null

  return (
    <Segmented
      size="small"
      value={reportId}
      onChange={(v) => onPick(Number(v))}
      options={versions.map((v) => ({
        value: v.id,
        label: (
          // The generation time is on the tooltip rather than the label: the forms come from separate
          // runs and can legitimately differ in age, which a reader comparing them needs to know, but
          // it is not what they are choosing between.
          <Tooltip title={v.time ? t('version.generatedAt', { when: formatReportDateTime(v.time) }) : undefined}>
            <span>{v.label}</span>
          </Tooltip>
        ),
      }))}
    />
  )
}
