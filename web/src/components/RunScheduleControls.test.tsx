import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import RunScheduleControls from './RunScheduleControls'
import type { RunSchedule } from '../lib/runSchedule'
import type { BatchTickets, RunPreset } from '../api/types'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

const baseValue: RunSchedule = { mode: 'now', runAt: null, idle: false, urgent: false }
const preset: RunPreset = {
  id: 1,
  label: 'Off-peak',
  freq: 'daily',
  intervals: [],
  on_overrun: 'next',
  enabled: true,
  invert: false,
  ord: 0,
}

function setup(
  overrides: { value?: RunSchedule; presets?: RunPreset[]; tickets?: BatchTickets | null } = {},
) {
  const onChange = vi.fn()
  render(
    <RunScheduleControls
      value={overrides.value ?? baseValue}
      onChange={onChange}
      presets={overrides.presets ?? []}
      tickets={overrides.tickets ?? { unlimited: true }}
    />,
  )
  return { onChange }
}

describe('RunScheduleControls', () => {
  it('hides the preset mode button when no presets are configured', () => {
    setup({ presets: [] })
    expect(screen.queryByText('run.preset')).toBeNull()
    expect(screen.getByText('run.now')).toBeTruthy()
    expect(screen.getByText('run.scheduled')).toBeTruthy()
  })

  it('shows the preset mode button when presets exist', () => {
    setup({ presets: [preset] })
    expect(screen.getByText('run.preset')).toBeTruthy()
  })

  it('renders the priority lanes as buttons and toggles urgent on click', async () => {
    const user = userEvent.setup()
    const { onChange } = setup()
    const urgent = screen.getByRole('button', { name: /run\.urgent/ })
    await user.click(urgent)
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ urgent: true, idle: false }))
  })

  it('toggles the idle lane on click in immediate mode', async () => {
    const user = userEvent.setup()
    const { onChange } = setup()
    await user.click(screen.getByRole('button', { name: /run\.idle/ }))
    expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ idle: true, urgent: false }))
  })

  it('offers the idle lane only in immediate mode', () => {
    setup({ value: { ...baseValue, mode: 'scheduled' } })
    expect(screen.queryByText('run.idle')).toBeNull()
  })

  it('hides the urgent lane when urgent is disabled for the group', () => {
    setup({ tickets: { unlimited: false, urgent_enabled: false } })
    expect(screen.queryByText(/run\.urgent/)).toBeNull()
  })

  it('folds the remaining ticket count into the urgent button label', () => {
    setup({ tickets: { unlimited: false, remaining: 2, allocation: 5 } })
    expect(screen.getByRole('button', { name: /run\.urgent 2\/5/ })).toBeTruthy()
  })

  it('disables the urgent lane button when the group is out of tickets', () => {
    setup({ tickets: { unlimited: false, remaining: 0, allocation: 5 } })
    const urgent = screen.getByRole('button', { name: /run\.urgent 0\/5/ }) as HTMLButtonElement
    expect(urgent.disabled).toBe(true)
  })
})
