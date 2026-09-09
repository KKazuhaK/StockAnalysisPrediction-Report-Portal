import { App as AntdApp } from 'antd'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, useLocation } from 'react-router'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { FavoriteItem } from '../api/types'
import FavoritesGrid, { moveFavoriteItems } from './FavoritesGrid'

const quoteCalls: string[][] = []

vi.mock('../lib/useHomeQuotes', () => ({
  useHomeQuotes: (symbols: string[]) => {
    quoteCalls.push([...symbols])
    return new Map()
  },
}))
vi.mock('./FavoriteButton', () => ({
  FavoriteButton: ({ market, symbol }: { market: string; symbol: string }) => (
    <button aria-label={`star-${market}${symbol}`} />
  ),
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }))

function item(index: number): FavoriteItem {
  const symbol = String(600000 + index)
  return { market: 'sh', symbol, key: `sh${symbol}`, ord: index }
}

function Location() {
  return <span data-testid="location">{useLocation().pathname + useLocation().search}</span>
}

describe('FavoritesGrid', () => {
  beforeEach(() => {
    quoteCalls.length = 0
  })

  it('requests quotes only for the favorites on the current page', async () => {
    render(
      <AntdApp>
        <MemoryRouter>
          <FavoritesGrid items={Array.from({ length: 21 }, (_, index) => item(index))} onReorder={vi.fn()} />
        </MemoryRouter>
      </AntdApp>,
    )

    await waitFor(() => expect(quoteCalls.some((call) => call.length === 20)).toBe(true))
    const firstPage = quoteCalls.find((call) => call.length === 20)!
    expect(firstPage[0]).toBe('sh:600000')
    expect(firstPage).not.toContain('sh:600020')

    await userEvent.click(screen.getByTitle('Next Page'))
    await waitFor(() => expect(quoteCalls.some((call) => call.length === 1 && call[0] === 'sh:600020')).toBe(true))
  })

  it('opens a quote-only favorite in the Quotes app', async () => {
    const favorite: FavoriteItem = { market: 'us', symbol: 'AAPL', key: 'usAAPL', ord: 0 }
    render(
      <AntdApp>
        <MemoryRouter>
          <FavoritesGrid items={[favorite]} onReorder={vi.fn()} />
          <Location />
        </MemoryRouter>
      </AntdApp>,
    )

    await userEvent.click(screen.getByText('AAPL'))
    expect(screen.getByTestId('location').textContent).toBe('/apps/quotes?symbol=us%3AAAPL')
  })

  it('moves an item in the full list while preserving every favorite', () => {
    const items = [item(0), item(1), item(2), item(3)]
    expect(moveFavoriteItems(items, items[3].key, items[1].key).map((x) => x.key)).toEqual([
      items[0].key,
      items[3].key,
      items[1].key,
      items[2].key,
    ])
  })
})
