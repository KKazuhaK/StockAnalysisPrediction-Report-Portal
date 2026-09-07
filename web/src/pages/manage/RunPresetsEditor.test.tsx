import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import RunPresetsEditor from './RunPresetsEditor'
import type { RunPreset } from '../../api/types'

// A weekly preset is stored as one interval per occurrence — "Mon and Wed, 09:00–12:00" is two
// rows on the wire. The editor's job is to show that back as the one thing the admin configured:
// a pair of weekdays sharing a time range, editable in one place, and expanded again on save.
const weeklyPreset: RunPreset = {
  id: 7,
  label: 'Off-peak',
  freq: 'weekly',
  intervals: [
    { start: { weekday: 1, time: '09:00' }, stop: { weekday: 1, time: '12:00' } },
    { start: { weekday: 3, time: '09:00' }, stop: { weekday: 3, time: '12:00' } },
  ],
  on_overrun: 'next',
  enabled: true,
  invert: false,
  ord: 0,
}

const sent = vi.hoisted(() => ({ calls: [] as Array<{ url: string; body: unknown }> }))
vi.mock('../../api/client', () => ({
  api: {
    get: () => Promise.resolve({ presets: [weeklyPreset] }),
    post: (url: string, body: unknown) => {
      sent.calls.push({ url, body })
      return Promise.resolve({})
    },
    put: (url: string, body: unknown) => {
      sent.calls.push({ url, body })
      return Promise.resolve({})
    },
    del: () => Promise.resolve({}),
  },
  errText: () => 'boom',
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const openEditor = async () => {
  const user = userEvent.setup()
  render(
    <App>
      <RunPresetsEditor />
    </App>,
  )
  await screen.findByText('Off-peak')
  await user.click(screen.getByRole('button', { name: 'common.edit' }))
  await screen.findByText('preset.edit')
  return user
}

describe('RunPresetsEditor', () => {
  beforeEach(() => {
    sent.calls.length = 0
  })

  it('shows a weekly preset as one row holding both of its weekdays', async () => {
    await openEditor()
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByText('run.weekday.1')).toBeTruthy()
    expect(within(dialog).getByText('run.weekday.3')).toBeTruthy()
    // The per-edge weekday pickers are what the row replaces: one control now holds both days.
    expect(within(dialog).queryByText('run.weekday.2')).toBeNull()
  })

  it('saves the row back as one interval per weekday, unchanged', async () => {
    const user = await openEditor()
    await user.click(screen.getByRole('button', { name: 'common.save' }))
    await waitFor(() => expect(sent.calls.length).toBeGreaterThan(0))
    const body = sent.calls[0].body as { intervals: unknown }
    expect(sent.calls[0].url).toBe('/api/admin/batch/presets/7')
    expect(body.intervals).toEqual(weeklyPreset.intervals)
  })
})
