import { describe, it, expect, vi, beforeEach } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'
import BatchConsole from './BatchConsole'

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('../api/client', () => ({ api: apiMock, errText: (e: unknown) => String((e as Error)?.message ?? e) }))
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

  // "No runnable targets yet. Ask an admin to configure a target" is advice about the server,
  // and the console used to give it for the whole flight of the first request.
  it('says it is loading instead of declaring there is nothing to run', async () => {
    apiMock.get.mockImplementation((url: string) =>
      url.includes('/batch/targets') ? new Promise(() => {}) : Promise.resolve({}),
    )
    render(
      <App>
        <BatchConsole />
      </App>,
    )
    expect(await screen.findByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('batch.noTargets')).toBeNull()
    expect(screen.queryByRole('combobox')).toBeNull()
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
