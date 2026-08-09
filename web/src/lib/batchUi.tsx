import { Tag, Tooltip, Typography } from 'antd'
import type { CSSProperties } from 'react'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'

import { ALL_SURFACES } from '../api/types'
import type { BatchTarget, Surface } from '../api/types'

// Shared presentation for run/queue views. Priority is a Slurm-style number now, not a
// tier: a run stores "urgent" or a base number 0..100 (docs/adr/0008-multifactor-priority.md).

export const BASE_MAX = 100
export const JOB_STATUS_COLOR: Record<string, string> = {
  queued: 'default',
  running: 'processing',
  cancelling: 'warning',
  cancelled: 'default',
  finished: 'success',
  // 'untracked': the run reached Dify but its outcome couldn't be confirmed — neutral gold (not a
  // red failure), and never re-run to avoid a duplicate charged run. See docs/adr/0015-reconcile-not-retry-untracked.md.
  untracked: 'warning',
}

export function statusTag(t: TFunction, s: string) {
  const tag = <Tag color={JOB_STATUS_COLOR[s] || 'default'}>{t(`batch.status.${s}`)}</Tag>
  // 'untracked' is unusual enough to warrant an inline explanation on hover.
  if (s === 'untracked') {
    return <Tooltip title={t('batch.status.untrackedHint')}>{tag}</Tooltip>
  }
  return tag
}

// isUrgent reports whether a stored priority is the urgent escalation.
export function isUrgent(p?: string) {
  return p === 'urgent'
}

// priorityNum reads a stored priority as its base number (mirrors the backend's
// parsePriority): the legacy tier names map (normal→50, other→20), a number is clamped.
export function priorityNum(p?: string): number {
  if (p == null || p === '' || p === 'normal') return 50
  if (p === 'other') return 20
  const n = Number(p)
  if (!Number.isFinite(n)) return 50
  return Math.min(BASE_MAX, Math.max(0, Math.round(n)))
}

// baseTagColor tints a base number so higher priorities read warmer at a glance.
function baseTagColor(n: number): string {
  if (n >= 67) return 'volcano'
  if (n >= 34) return 'blue'
  return 'default'
}

// priorityTag renders a run's priority: a red urgent tag, else the base number tinted by
// magnitude.
export function priorityTag(t: TFunction, p?: string) {
  if (isUrgent(p)) return <Tag color="red">{t('batch.priority.urgent')}</Tag>
  const n = priorityNum(p)
  return <Tag color={baseTagColor(n)}>{n}</Tag>
}

// inputPairs reads a run's inputs JSON as [key, value] pairs, dropping empty values (e.g. an
// unfilled optional field). A body that isn't a JSON object is kept as one unnamed value.
function inputPairs(s?: string): Array<[string, string]> {
  if (!s) return []
  let parsed: unknown
  try {
    parsed = JSON.parse(s)
  } catch {
    return [['', s]]
  }
  if (parsed == null || typeof parsed !== 'object') return [['', s]]
  return Object.entries(parsed as Record<string, unknown>)
    .filter(([, v]) => v !== '' && v != null)
    .map(([k, v]) => [k, String(v)] as [string, string])
}

const joinPair = (k: string, v: string) => (k ? `${k}=${v}` : v)

// fmtInputs renders a run's first-row inputs JSON as "key=value  key=value",
// dropping empty values (e.g. an unfilled optional field). Lossless — it backs the queue
// search and the run-detail modal, which must show exactly what was submitted.
export function fmtInputs(s?: string) {
  if (!s) return ''
  return inputPairs(s)
    .map(([k, v]) => joinPair(k, v))
    .join('  ')
}

// ---------------------------------------------------------------------------
// Inputs preview budget
// ---------------------------------------------------------------------------
// A run's inputs are arbitrary text: a chat/agent run carries its whole prompt in `query`,
// routinely thousands of characters. Poured into a hover tooltip verbatim, the overlay grows
// past the viewport — the tail is unreadable, and once the overlay reaches under the cursor
// the hover toggles itself off and on (the tooltip flickers). So the preview is clamped on
// two levels:
//
//   level 1 (per entry)  — every value is clamped on its own, so one runaway `query` cannot
//                          push `symbol=301539` out of the preview;
//   level 2 (whole list) — the kept entries are capped by count and by total length, with the
//                          remainder reported as a trailing "+N".
//
// totalMax is a budget, not a hard limit: the entry that crosses it is kept whole (already
// clamped by level 1) rather than cut a second time, and the first entry is always kept so a
// preview is never empty. Worst case is therefore ~370 characters — about 15 tooltip lines,
// which fits beside the cursor in any viewport. The untruncated inputs stay one click away in
// the run's detail modal.
export const INPUT_VALUE_MAX = 80
export const INPUT_ENTRY_MAX = 8
export const INPUT_TOTAL_MAX = 280

const ELLIPSIS = '…'

// Roomier than antd's 250px default so a clamped preview needs fewer lines, still narrow
// enough that the overlay sits beside the cursor rather than under it. maxHeight is the
// backstop the old preview lacked: whatever the content, the overlay can never grow taller
// than the viewport (and start toggling its own hover) again.
export const TOOLTIP_STYLES = {
  root: { maxWidth: 360 },
  container: { maxHeight: 320, overflowY: 'auto' as const },
}

// clampText collapses whitespace (a multi-line prompt must not stretch the preview vertically)
// and cuts to `max` characters, marking the cut with an ellipsis.
export function clampText(v: string, max: number): string {
  const flat = v.replace(/\s+/g, ' ').trim()
  return flat.length > max ? `${flat.slice(0, max)}${ELLIPSIS}` : flat
}

export type InputsSummary = {
  entries: string[] // clamped "key=value" entries, in submission order
  hidden: number // entries dropped by the whole-list cap
}

export function summarizeInputs(
  inputs?: string,
  opts: { valueMax?: number; entryMax?: number; totalMax?: number } = {},
): InputsSummary {
  const valueMax = opts.valueMax ?? INPUT_VALUE_MAX
  const entryMax = opts.entryMax ?? INPUT_ENTRY_MAX
  const totalMax = opts.totalMax ?? INPUT_TOTAL_MAX
  const pairs = inputPairs(inputs)
  const entries: string[] = []
  let used = 0
  for (const [k, v] of pairs) {
    if (entries.length > 0 && (entries.length >= entryMax || used >= totalMax)) break
    const entry = joinPair(k, clampText(v, valueMax))
    entries.push(entry)
    used += entry.length
  }
  return { entries, hidden: pairs.length - entries.length }
}

// InputsPreview renders a run's inputs as a compact, clamped label: the summary (see
// summarizeInputs) on one line, and the same entries one-per-line in the hover tooltip.
// Returns null when there are no inputs so callers can drop the line entirely.
export function InputsPreview({
  inputs,
  rows = 2,
  secondary = true,
  style,
}: {
  inputs?: string
  rows?: number
  secondary?: boolean
  style?: CSSProperties
}) {
  const { t } = useTranslation()
  const { entries, hidden } = summarizeInputs(inputs)
  if (entries.length === 0) return null
  const lines = hidden > 0 ? [...entries, t('batch.inputsMore', { n: hidden })] : entries
  return (
    <Typography.Paragraph
      type={secondary ? 'secondary' : undefined}
      ellipsis={{
        rows,
        // One entry per line reads far better than a wrapped run-on, and the bounded content
        // keeps the overlay small enough that it never lands under the cursor.
        tooltip: { title: <div style={{ whiteSpace: 'pre-wrap' }}>{lines.join('\n')}</div>, styles: TOOLTIP_STYLES },
      }}
      style={{ fontSize: 12, marginBottom: 0, ...style }}
    >
      {lines.join('  ')}
    </Typography.Paragraph>
  )
}

export function isTerminal(status: string) {
  return status === 'finished' || status === 'cancelled'
}

// difyModeKind maps a Dify target's raw app mode to the three run types the portal
// supports: "workflow" (/workflows/run), or the two conversational types "agent" and
// "chat" (both run via /chat-messages, with the row's `query` column as the message).
// Dify reports "workflow", "agent-chat", "chat"/"advanced-chat", etc.
export function difyModeKind(mode?: string): 'workflow' | 'agent' | 'chat' {
  if (!mode || mode === 'workflow') return 'workflow'
  if (mode.includes('agent')) return 'agent'
  return 'chat'
}

// difyModeTag labels a target by its Dify app mode so every target reads at a glance:
// Workflow (cyan), Agent (purple), or Chat (geekblue).
export function difyModeTag(t: TFunction, mode?: string) {
  const kind = difyModeKind(mode)
  const color = kind === 'agent' ? 'purple' : kind === 'chat' ? 'geekblue' : 'cyan'
  return <Tag color={color}>{t(`batch.dify.${kind}Tag`)}</Tag>
}

// ---------------------------------------------------------------------------
// Surface visibility
// ---------------------------------------------------------------------------
// Two independent rules decide whether a target appears on a surface, and keeping them
// apart is the point:
//
//   capability — what the app CAN do. An agent-chat app holds a conversation and never
//                posts a report, so it can never serve 运行分析/批量执行 no matter what an
//                admin ticks. A workflow app is not conversational, so it cannot serve 助手.
//   policy     — what the admin WANTS. "Only offer this one on 计划任务."
//
// Effective visibility is capability AND policy. Merging them into one flag would mean an
// admin could tick a box that silently does nothing, or that adding a Dify app mode would
// have to rewrite everyone's saved policy.
//
// Defined once here because five call sites read it (RunAnalysisModal, BatchConsole,
// RecurringConsole, ChatPage, BatchAdminPage). The previous single rule lived inline in
// RunAnalysisModal; a second copy would drift the first time a mode is added.

export function surfaceSupportsMode(surface: Surface, mode?: string): boolean {
  const kind = difyModeKind(mode)
  if (surface === 'chat') return kind !== 'workflow'
  return kind !== 'agent'
}

// The admin's allow-list. The API resolves "unset" to all four before sending, so a missing
// field here means an old client/response shape — treat it as unrestricted, never as hidden:
// failing closed would make every target vanish from every surface on a version skew.
export function policyAllows(tg: BatchTarget, surface: Surface): boolean {
  const list = tg.surfaces && tg.surfaces.length ? tg.surfaces : ALL_SURFACES
  return list.includes(surface)
}

export function visibleOn(targets: BatchTarget[], surface: Surface): BatchTarget[] {
  return targets.filter((tg) => surfaceSupportsMode(surface, tg.mode) && policyAllows(tg, surface))
}
