import { type ReactNode } from 'react'
import { Checkbox, DatePicker, Grid, Radio, Select, Space, Tag, Typography } from 'antd'
import { ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { BatchTickets, RunPreset } from '../api/types'
import { presetSummary, type RunSchedule } from '../lib/runSchedule'

// The shared run-time + priority control for both the single-run modal and the batch console
// (docs/adr/0014-idle-lane-and-preset-windows.md): a three-way mode toggle (立即 | 预设 | 定时),
// the matching preset dropdown / date picker, and the mutually-exclusive 加急 / 队列空闲 lanes
// (idle only offered in immediate mode). On a phone the picker drops to its own full-width row
// instead of crowding the buttons.
export default function RunScheduleControls({
  value,
  onChange,
  presets,
  tickets,
  disabled,
}: {
  value: RunSchedule
  onChange: (v: RunSchedule) => void
  presets: RunPreset[] // enabled presets for the dropdown
  tickets: BatchTickets | null
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const mobile = !Grid.useBreakpoint().md
  const urgentEnabled = tickets?.urgent_enabled !== false
  // Urgent runs need a ticket unless the user's group is unlimited; disable at 0.
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0

  const set = (patch: Partial<RunSchedule>) => onChange({ ...value, ...patch })
  // Idle is only meaningful in immediate mode — leaving "now" clears it.
  const setMode = (mode: RunSchedule['mode']) => set({ mode, idle: mode === 'now' ? value.idle : false })

  // The date/preset picker for the chosen mode (nothing in "now").
  let picker: ReactNode = null
  if (value.mode === 'scheduled') {
    picker = (
      <DatePicker
        showTime
        value={value.runAt}
        onChange={(d) => set({ runAt: d })}
        disabled={disabled}
        format="YYYY-MM-DD HH:mm:ss"
        placeholder={t('run.pickTime')}
        style={{ width: mobile ? '100%' : undefined }}
      />
    )
  } else if (value.mode === 'preset') {
    picker = presets.length ? (
      <Select
        style={{ minWidth: 240, width: mobile ? '100%' : undefined }}
        placeholder={t('run.pickPreset')}
        value={value.presetId}
        disabled={disabled}
        onChange={(id) => set({ presetId: id })}
        options={presets.map((p) => ({ value: p.id, label: `${p.label} · ${presetSummary(p, t)}` }))}
      />
    ) : (
      <Typography.Text type="secondary">{t('run.noPresets')}</Typography.Text>
    )
  }

  return (
    <Space direction="vertical" size={8} style={{ width: '100%' }}>
      <div style={{ display: 'flex', gap: 10, flexWrap: 'wrap', alignItems: 'center' }}>
        <Radio.Group
          value={value.mode}
          onChange={(e) => setMode(e.target.value)}
          optionType="button"
          buttonStyle="solid"
          disabled={disabled}
        >
          <Radio.Button value="now">{t('run.now')}</Radio.Button>
          <Radio.Button value="preset">{t('run.preset')}</Radio.Button>
          <Radio.Button value="scheduled">{t('run.scheduled')}</Radio.Button>
        </Radio.Group>
        {!mobile && picker}
      </div>
      {mobile && picker && <div style={{ width: '100%' }}>{picker}</div>}

      {(value.mode === 'now' || urgentEnabled) && (
        <Space wrap>
          {value.mode === 'now' && (
            <Checkbox
              checked={value.idle}
              disabled={disabled || value.urgent}
              onChange={(e) => set({ idle: e.target.checked, urgent: e.target.checked ? false : value.urgent })}
            >
              {t('run.idle')}
            </Checkbox>
          )}
          {urgentEnabled && (
            <>
              <Checkbox
                checked={value.urgent}
                disabled={disabled || urgentDisabled || value.idle}
                onChange={(e) => set({ urgent: e.target.checked, idle: e.target.checked ? false : value.idle })}
              >
                {t('run.urgent')}
              </Checkbox>
              {tickets && !tickets.unlimited && (
                <Tag color={(tickets.remaining ?? 0) > 0 ? 'gold' : 'default'} icon={<ThunderboltOutlined />}>
                  {t('batch.ticketsLeft', { n: tickets.remaining ?? 0, total: tickets.allocation ?? 0 })}
                </Tag>
              )}
              {urgentDisabled && (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t('run.noTickets')}
                </Typography.Text>
              )}
            </>
          )}
        </Space>
      )}
    </Space>
  )
}
