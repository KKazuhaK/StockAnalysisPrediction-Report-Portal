import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import BatchConsole from './BatchConsole'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('../api/client', () => ({ api: apiMock }))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../auth', () => ({ useAuth: () => ({ admin: true, email: '', mailEnabled: false }) }))
vi.mock('../components/RunScheduleControls', () => ({ default: () => null }))
vi.mock('../components/QueueTable', () => ({ default: () => null }))

describe('BatchConsole', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url.includes('/batch/targets'))
        return Promise.resolve({
          targets: [{ id: 1, plugin_slug: 'dify', name: 'Research', created_at: '', inputs: [{ key: 'code' }, { key: 'query' }] }],
        })
      if (url.includes('/batch/tickets')) return Promise.resolve({ urgent_enabled: true, unlimited: true })
      if (url.includes('/batch/presets')) return Promise.resolve({ presets: [], default_mode: 'now', default_idle: false })
      return Promise.resolve({})
    })
  })

  it('prefills the CSV header when a target is selected', async () => {
    const user = userEvent.setup()
    render(
      <App>
        <BatchConsole />
      </App>,
    )

    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/admin/batch/targets'))
    fireEvent.mouseDown(screen.getByRole('combobox'))
    await user.click(await screen.findByText('Research'))

    const editor = screen.getByPlaceholderText('batch.csvPlaceholder') as HTMLTextAreaElement
    expect(editor.value).toBe('code,query')
  })
})
