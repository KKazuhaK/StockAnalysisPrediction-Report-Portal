import { App } from 'antd'
import { fireEvent, render, screen, within } from '@testing-library/react'
import { MemoryRouter, useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Group } from '../api/types'
import ReportCard from './ReportCard'

const favoriteState = vi.hoisted(() => ({ active: false, toggle: vi.fn().mockResolvedValue(undefined) }))

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))
vi.mock('../api/client', () => ({ errText: (cause: unknown) => String(cause) }))
vi.mock('../favorites', () => ({
  useFavorites: () => ({
    loaded: true,
    loading: false,
    error: null,
    reordering: false,
    ensureLoaded: vi.fn(),
    isFavorite: () => favoriteState.active,
    isBusy: () => false,
    toggle: favoriteState.toggle,
  }),
}))

const report: Group = {
  key: '603075|2026-09-08',
  symbol: '603075',
  market: 'sh',
  name: 'Test stock',
  date: '2026-09-08',
  kind: 'Decision',
  kinds: ['Decision'],
  src: 'new',
  n: 4,
  members: [
    { id: 1, rtype: 'Decision', kind: 'Decision', title: 'Preview report one' },
    { id: 2, rtype: 'Investment', kind: 'Investment', title: 'Preview report two' },
    { id: 3, rtype: 'Research', kind: 'Research', title: 'Preview report three' },
    { id: 4, rtype: 'Technical', kind: 'Technical', title: 'Preview report four' },
  ],
}

function mount(g: Group = report) {
  return render(
    <App>
      <MemoryRouter>
        <ReportCard g={g} />
        <LocationReadout />
      </MemoryRouter>
    </App>,
  )
}

function LocationReadout() {
  const location = useLocation()
  return <output data-testid="location">{location.pathname}{location.search}</output>
}

describe('ReportCard favorite action', () => {
  beforeEach(() => {
    favoriteState.active = false
    favoriteState.toggle.mockClear()
  })

  it('keeps the symbol and favorite in a dedicated trailing action slot', () => {
    mount()

    const button = screen.getByRole('button', { name: 'favorite.add' })
    const slot = button.closest('.rp-card-symbol-actions')
    expect(slot).not.toBeNull()
    expect(slot?.textContent).toContain(report.symbol)
    expect(button.classList).toContain('rp-card-favorite')
  })

  it('expands the current card after a delay with three summaries and quick actions', async () => {
    mount()

    const card = screen.getByRole('button', { name: 'Test stock' })
    fireEvent.mouseEnter(card)
    expect(screen.queryByTestId('report-card-preview')).toBeNull()

    const preview = await screen.findByTestId('report-card-preview', {}, { timeout: 2000 })
    expect(card.contains(preview)).toBe(true)
    expect(card.classList).toContain('rp-report-card--expanded')
    expect(document.querySelector('.ant-popover')).toBeNull()
    expect(within(preview).getByText('Preview report one')).toBeTruthy()
    expect(within(preview).getByText('Preview report two')).toBeTruthy()
    expect(within(preview).getByText('Preview report three')).toBeTruthy()
    expect(screen.queryByText('Preview report four')).toBeNull()
    expect(within(preview).getByText('+1')).toBeTruthy()
    const viewReport = within(preview).getByRole('button', { name: 'queue.viewReport' })
    expect(viewReport).toBeTruthy()

    fireEvent.click(within(preview).getByRole('button', { name: 'favorite.add' }))
    expect(favoriteState.toggle).toHaveBeenCalledWith('sh', '603075')

    fireEvent.click(viewReport)
    expect(screen.getByTestId('location').textContent).toBe('/stock/603075?date=2026-09-08')
  })
})
