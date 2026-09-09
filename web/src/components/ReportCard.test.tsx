import { App } from 'antd'
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Group } from '../api/types'
import ReportCard from './ReportCard'

const favoriteState = vi.hoisted(() => ({ active: false }))

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
    toggle: vi.fn().mockResolvedValue(undefined),
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
  n: 1,
  members: [],
}

function mount() {
  return render(
    <App>
      <MemoryRouter>
        <ReportCard g={report} />
      </MemoryRouter>
    </App>,
  )
}

describe('ReportCard favorite action', () => {
  beforeEach(() => {
    favoriteState.active = false
  })

  it('keeps the symbol and favorite in a dedicated trailing action slot', () => {
    mount()

    const button = screen.getByRole('button', { name: 'favorite.add' })
    const slot = button.closest('.rp-card-symbol-actions')
    expect(slot).not.toBeNull()
    expect(slot?.textContent).toContain(report.symbol)
    expect(button.classList).toContain('rp-card-favorite')
  })
})
