import { useEffect, useState } from 'react'
import {
  App,
  Button,
  Drawer,
  Empty,
  List,
  Popconfirm,
  Segmented,
  Space,
  Spin,
  Tag,
  Typography,
  theme,
} from 'antd'
import { RollbackOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import type { SectionDiff } from '../api/types'
import { formatReportDateTime } from '../lib/datetime'
import Markdown from './Markdown'
import { SectionBlock } from './DiffSections'

// The edit history of a hand-written report (ADR 0026).
//
// It shows what the report USED to say. The current text is not in the list because it is not
// history — it is the report, and it sits above the list as the thing every entry is compared
// against.
//
// Two views of one prior version, and both earn their place. The diff answers "what changed", which
// is the question a history is opened to ask. The document view answers the one asked immediately
// before pressing restore: an author about to overwrite the current text wants to read the old one
// as prose, with its tables and formulas rendered, not as monospace lines with markers.
//
// This drawer never touches the editor's own state. The unsaved draft in the textarea lives only in
// React state and has no other copy, so previewing a version renders it into this surface and a
// restore reports back through onRestored rather than writing anything itself.

export interface RevisionSummary {
  id: number
  savedAt: string
  author: string
  title: string
  bytes: number
}

interface RevisionDetail {
  revision: RevisionSummary & { body_md: string }
  sections: SectionDiff[]
  changed: number
}

export default function ReportHistoryDrawer({
  reportId,
  token,
  open,
  onClose,
  onRestored,
}: {
  reportId: number
  /**
   * The concurrency token the EDITOR is holding. Sent with a restore so one computed from a list
   * drawn before somebody else saved is refused rather than silently winning — the same rule an
   * ordinary save follows, because a restore is one.
   */
  token: string
  open: boolean
  onClose: () => void
  /** Hands back the report's fresh token so the editor can keep working without a reload. */
  onRestored: (updatedAt: string) => void
}) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { token: theme_ } = theme.useToken()
  const [loading, setLoading] = useState(false)
  const [list, setList] = useState<RevisionSummary[] | null>(null)
  const [current, setCurrent] = useState<{ savedAt: string; author: string; title: string } | null>(null)
  const [keep, setKeep] = useState(0)
  const [picked, setPicked] = useState<number | null>(null)
  const [detail, setDetail] = useState<RevisionDetail | null>(null)
  const [view, setView] = useState<'diff' | 'doc'>('diff')
  const [restoring, setRestoring] = useState(false)

  // Reloaded on every open rather than cached: the list is a claim about what the report has been,
  // and a drawer reopened after a save would otherwise show a history missing that save.
  useEffect(() => {
    if (!open) return
    let live = true
    setLoading(true)
    setPicked(null)
    setDetail(null)
    api
      .get<{ revisions: RevisionSummary[]; current: typeof current; keep: number }>(
        `/api/reports/${reportId}/revisions`,
      )
      .then((r) => {
        if (!live) return
        setList(r.revisions ?? [])
        setCurrent(r.current ?? null)
        setKeep(r.keep ?? 0)
      })
      .catch((e) => live && message.error(errText(e, t)))
      .finally(() => live && setLoading(false))
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, reportId])

  const pick = (id: number) => {
    setPicked(id)
    setDetail(null)
    api
      .get<RevisionDetail>(`/api/reports/${reportId}/revisions/${id}`)
      .then(setDetail)
      .catch((e) => message.error(errText(e, t)))
  }

  const restore = async (id: number) => {
    setRestoring(true)
    try {
      const r = await api.post<{ updated_at: string }>(
        `/api/reports/${reportId}/revisions/${id}/restore`,
        { updated_at: token },
      )
      message.success(t('reportHistory.restored'))
      onRestored(r.updated_at)
      onClose()
    } catch (e) {
      message.error(errText(e, t))
    } finally {
      setRestoring(false)
    }
  }

  const author = (name: string) => name || t('reportHistory.unknownAuthor')

  return (
    <Drawer title={t('reportHistory.title')} open={open} onClose={onClose} width={720} destroyOnHidden>
      <Spin spinning={loading}>
        <Space direction="vertical" size={16} style={{ width: '100%' }}>
          {current && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('reportHistory.currentLine', {
                when: formatReportDateTime(current.savedAt),
                who: author(current.author),
              })}
            </Typography.Text>
          )}

          {list && list.length === 0 ? (
            <Empty description={t('reportHistory.empty')} />
          ) : (
            <List
              size="small"
              bordered
              dataSource={list ?? []}
              renderItem={(v) => (
                <List.Item
                  onClick={() => pick(v.id)}
                  style={{
                    cursor: 'pointer',
                    background: v.id === picked ? theme_.controlItemBgActive : undefined,
                  }}
                  actions={[
                    <Popconfirm
                      key="restore"
                      title={t('reportHistory.restoreConfirm')}
                      onConfirm={() => restore(v.id)}
                    >
                      <Button
                        size="small"
                        icon={<RollbackOutlined />}
                        loading={restoring}
                        onClick={(e) => e.stopPropagation()}
                      >
                        {t('reportHistory.restore')}
                      </Button>
                    </Popconfirm>,
                  ]}
                >
                  <List.Item.Meta
                    title={
                      <Space size={8} wrap>
                        <span>{formatReportDateTime(v.savedAt)}</span>
                        <Tag>{author(v.author)}</Tag>
                      </Space>
                    }
                    description={
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {v.title} · {t('reportHistory.bytes', { count: v.bytes })}
                      </Typography.Text>
                    }
                  />
                </List.Item>
              )}
            />
          )}

          {keep > 0 && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t('reportHistory.capped', { count: keep })}
            </Typography.Text>
          )}

          {picked !== null && (
            <Space direction="vertical" size={8} style={{ width: '100%' }}>
              <Segmented
                size="small"
                value={view}
                onChange={(v) => setView(v as 'diff' | 'doc')}
                options={[
                  { value: 'diff', label: t('reportHistory.viewDiff') },
                  { value: 'doc', label: t('reportHistory.viewDoc') },
                ]}
              />
              {!detail ? (
                <Spin />
              ) : view === 'diff' ? (
                detail.changed === 0 ? (
                  <Empty description={t('reportHistory.noChange')} />
                ) : (
                  <div>
                    {detail.sections
                      .filter((sec) => sec.status !== 'same')
                      .map((sec, i) => (
                        <SectionBlock key={i} sec={sec} />
                      ))}
                  </div>
                )
              ) : (
                <Markdown md={detail.revision.body_md} />
              )}
            </Space>
          )}
        </Space>
      </Spin>
    </Drawer>
  )
}
