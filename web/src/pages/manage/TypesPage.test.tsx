import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import TypesPage from './TypesPage'

const post = vi.fn()

vi.mock('../../api/client', () => ({
  api: {
    get: () => Promise.resolve({ groups: [], kinds: [] }),
    post: (...a: unknown[]) => post(...a),
    del: () => Promise.resolve({}),
  },
}))

// Echo the i18n key (plus interpolation) so buttons are findable by their key.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
  }),
}))

const renderPage = () =>
  render(
    <App>
      <TypesPage />
    </App>,
  )

describe('TypesPage — restore defaults', () => {
  beforeEach(() => post.mockReset())

  it('posts to the restore-defaults endpoint only after the confirm is accepted', async () => {
    post.mockResolvedValue({ restored: 26 })
    renderPage()

    fireEvent.click(await screen.findByText('types.restoreDefaults'))
    expect(post).not.toHaveBeenCalled() // Popconfirm gate: nothing fires on the first click

    fireEvent.click(await screen.findByText('OK')) // antd Popconfirm default confirm label
    await waitFor(() => expect(post).toHaveBeenCalledWith('/api/admin/types/restore-defaults', {}))
  })
})
