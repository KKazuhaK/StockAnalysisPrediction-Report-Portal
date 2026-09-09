import { App as AntdApp } from 'antd'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './api/client'
import { FavoriteButton } from './components/FavoriteButton'
import { FavoritesProvider, useFavorites } from './favorites'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}))

vi.mock('./api/client', async (load) => {
  const actual = await load<typeof import('./api/client')>()
  return { ...actual, api: apiMock }
})

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

function Probe() {
  const favorites = useFavorites()
  return (
    <>
      <span data-testid="loaded">{String(favorites.loaded)}</span>
      <span data-testid="member">{String(favorites.isFavorite('sh', '600519'))}</span>
      <button onClick={() => void favorites.toggle('sh', '600519').catch(() => {})}>toggle</button>
    </>
  )
}

function shell(child: React.ReactNode) {
  return render(
    <AntdApp>
      <FavoritesProvider user="alice">{child}</FavoritesProvider>
    </AntdApp>,
  )
}

describe('FavoritesProvider', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.put.mockReset()
    apiMock.del.mockReset()
    apiMock.get.mockResolvedValue({ items: [], limit: 500 })
  })

  it('adds optimistically and rolls back only that item when the write fails', async () => {
    const write = deferred<never>()
    apiMock.put.mockReturnValue(write.promise)
    shell(<Probe />)

    await waitFor(() => expect(screen.getByTestId('loaded').textContent).toBe('true'))
    await userEvent.click(screen.getByText('toggle'))
    expect(screen.getByTestId('member').textContent).toBe('true')
    expect(apiMock.put).toHaveBeenCalledWith('/api/favorites/sh/600519')

    await act(async () => write.reject(new ApiError(500, 'failed', 'favorite_write_failed')))
    await waitFor(() => expect(screen.getByTestId('member').textContent).toBe('false'))
  })

  it('removes optimistically and restores the original position when the write fails', async () => {
    apiMock.get.mockResolvedValue({
      items: [
        { market: 'sh', symbol: '600519', key: 'sh600519', ord: 0 },
        { market: 'sz', symbol: '000001', key: 'sz000001', ord: 1 },
      ],
      limit: 500,
    })
    const write = deferred<never>()
    apiMock.del.mockReturnValue(write.promise)
    shell(<Probe />)

    await waitFor(() => expect(screen.getByTestId('member').textContent).toBe('true'))
    await userEvent.click(screen.getByText('toggle'))
    expect(screen.getByTestId('member').textContent).toBe('false')
    expect(apiMock.del).toHaveBeenCalledWith('/api/favorites/sh/600519')

    await act(async () => write.reject(new Error('offline')))
    await waitFor(() => expect(screen.getByTestId('member').textContent).toBe('true'))
  })

  it('keeps the star click out of its parent card action', async () => {
    const open = vi.fn()
    apiMock.put.mockResolvedValue({ market: 'sh', symbol: '600519', key: 'sh600519', ord: 0 })
    shell(
      <div onClick={open}>
        <FavoriteButton market="sh" symbol="600519" />
      </div>,
    )

    const button = await screen.findByRole('button', { name: 'favorite.add' })
    await waitFor(() => expect(button.hasAttribute('disabled')).toBe(false))
    await userEvent.click(button)

    expect(open).not.toHaveBeenCalled()
    expect(button.getAttribute('aria-pressed')).toBe('true')
  })
})
