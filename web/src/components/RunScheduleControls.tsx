import { useEffect, type ReactNode } from 'react'
import { Button, DatePicker, Grid, Popover, Radio, Select, Space, Tooltip, Typography } from 'antd'
import { InfoCircleOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import type { BatchTickets, RunPreset } from '../api/types'
import { presetSummary, presetWindows, type RunSchedule } from '../lib/runSchedule'

// The shared run-time + priority control for both the single-run modal and the batch console
// (docs/adr/0014-idle-lane-and-preset-windows.md): a mode toggle (立即 | 预设 | 定时), the matching
// preset dropdown / date picker, and the mutually-exclusive 加急 / 队列空闲 priority lanes. The
// preset mode button only appears when the admin has enabled at least one preset window, and the
// two lanes are solid toggle buttons that match the mode toggle (idle only offered in immediate
// mode). On a phone the picker drops to its own full-width row instead of crowding the buttons.
//
// The preset picker shows the window's *name*; a timetable long enough to overrun the control does
// not belong in a control whose job is to say which window is chosen. The rule is still one glance
// away — under every option in the open drop-down, and in full behind the info button beside it —
// and an admin who wants it spelled out inline turns `showRule` on (Manage -> Run defaults).
export default function RunScheduleControls({
  value,
  onChange,
  presets,
  tickets,
  disabled,
  showRule,
}: {
  value: RunSchedule
  onChange: (v: RunSchedule) => void
  presets: RunPreset[] // enabled presets for the dropdown
  tickets: BatchTickets | null
  disabled?: boolean
  showRule?: boolean // admin setting: print the whole rule beside the window's name
}) {
  const { t } = useTranslation()
  const mobile = !Grid.useBreakpoint().md
  const urgentEnabled = tickets?.urgent_enabled !== false
  // Urgent runs need a ticket unless the user's group is unlimited; disable at 0.
  const urgentDisabled = urgentEnabled && tickets != null && !tickets.unlimited && (tickets.remaining ?? 0) <= 0
  const hasPresets = presets.length > 0

  const set = (patch: Partial<RunSchedule>) => onChange({ ...value, ...patch })
  // Idle is only meaningful in immediate mode — leaving "now" clears it.
  const setMode = (mode: RunSchedule['mode']) => set({ mode, idle: mode === 'now' ? value.idle : false })

  // A stale "preset" default (an admin picked it, then every preset was disabled) can't select any
  // button once the preset option is hidden — fall back to immediate so the control stays valid.
  useEffect(() => {
    if (value.mode === 'preset' && !hasPresets) onChange({ ...value, mode: 'now', idle: false })
  }, [value, hasPresets, onChange])

  // The date/preset picker for the chosen mode (nothing in "now").
  let picker: ReactNode = null
  if (value.mode === 'scheduled') {
    picker = (
      <DatePicker
        showTime={{ format: 'HH:mm' }}
        value={value.runAt}
        onChange={(d) => set({ runAt: d })}
        disabled={disabled}
        format="YYYY-MM-DD HH:mm"
        popupClassName="rp-picker-popup"
        placeholder={t('run.pickTime')}
        style={{ width: mobile ? '100%' : undefined }}
      />
    )
  } else if (value.mode === 'preset' && hasPresets) {
    const chosen = presets.find((p) => p.id === value.presetId)
    picker = (
      <div style={{ display: 'flex', alignItems: 'center', gap: 2, width: mobile ? '100%' : undefined }}>
        <Select
          style={{ minWidth: 240, flex: mobile ? 1 : undefined }}
          placeholder={t('run.pickPreset')}
          value={value.presetId}
          disabled={disabled}
          onChange={(id) => set({ presetId: id })}
          // The name is what the closed control shows; the rule rides under it in the open list, so
          // choosing a window never means choosing between names alone.
          optionRender={(o) => (
            <div>
              <div>{(o.data as { preset: RunPreset }).preset.label || t('preset.untitled')}</div>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {presetSummary((o.data as { preset: RunPreset }).preset, t)}
              </Typography.Text>
            </div>
          )}
          options={presets.map((p) => ({
            value: p.id,
            // The name now carries the option on its own, so an unnamed window has to fall back to
            // the same placeholder the admin list uses rather than rendering as a blank row.
            label: showRule ? `${p.label || t('preset.untitled')} · ${presetSummary(p, t)}` : p.label || t('preset.untitled'),
            preset: p,
          }))}
        />
        {chosen && (
          <Popover
            trigger="click"
            title={chosen.label}
            content={<PresetRule preset={chosen} />}
            placement={mobile ? 'bottom' : 'bottomLeft'}
          >
            <Button type="text" icon={<InfoCircleOutlined />} aria-label={t('preset.viewRule')} />
          </Popover>
        )}
      </div>
    )
  }

  // The remaining ticket count folds into the urgent button label (e.g. "加急运行 2/5") so a
  // separate tag + "out of tickets" hint aren't needed — the button just disables at zero.
  const ticketSuffix = tickets && !tickets.unlimited ? ` ${tickets.remaining ?? 0}/${tickets.allocation ?? 0}` : ''

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
          {hasPresets && <Radio.Button value="preset">{t('run.preset')}</Radio.Button>}
          <Radio.Button value="scheduled">{t('run.scheduled')}</Radio.Button>
        </Radio.Group>
        {!mobile && picker}
      </div>
      {mobile && picker && <div style={{ width: '100%' }}>{picker}</div>}

      {(value.mode === 'now' || urgentEnabled) && (
        <Space wrap size={8}>
          {value.mode === 'now' && (
            <Button
              type={value.idle ? 'primary' : 'default'}
              disabled={disabled || value.urgent}
              onClick={() => set({ idle: !value.idle, urgent: false })}
            >
              {t('run.idle')}
            </Button>
          )}
          {urgentEnabled && (
            <Tooltip title={urgentDisabled ? t('run.noTickets') : ''}>
              {/* A disabled antd button swallows hover events, so wrap it for the tooltip to show. */}
              <span style={{ display: 'inline-flex' }}>
                <Button
                  type={value.urgent ? 'primary' : 'default'}
                  icon={<ThunderboltOutlined />}
                  disabled={disabled || urgentDisabled || value.idle}
                  onClick={() => set({ urgent: !value.urgent, idle: false })}
                >
                  {t('run.urgent')}
                  {ticketSuffix}
                </Button>
              </span>
            </Tooltip>
          )}
        </Space>
      )}
    </Space>
  )
}

// PresetRule is the whole rule behind the picker's info button: the recurrence, every sub-window on
// its own line, and the policy that decides what happens to a run the window closed on. An inverted
// window lists the same times under "except", with the note that they are hours the run avoids.
function PresetRule({ preset }: { preset: RunPreset }) {
  const { t } = useTranslation()
  return (
    <Space direction="vertical" size={2} style={{ maxWidth: 300 }}>
      <Typography.Text type="secondary">
        {t('preset.freq')}: {t('run.freq.' + preset.freq)}
      </Typography.Text>
      <Typography.Text type="secondary">{preset.invert ? t('preset.summaryExcept') : t('preset.windows')}:</Typography.Text>
      {presetWindows(preset, t).map((w, i) => (
        <div key={i} style={{ paddingLeft: 8 }}>
          {w}
        </div>
      ))}
      {preset.invert ? (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t('preset.invertHint')}
        </Typography.Text>
      ) : (
        <Typography.Text type="secondary">
          {t('preset.overrunLabel')}: {t('preset.overrun.' + preset.on_overrun)}
        </Typography.Text>
      )}
    </Space>
  )
}
