import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { App, Grid } from 'antd'
import { MemoryRouter } from 'react-router'
import ChatPage from './ChatPage'

const apiMock = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  del: vi.fn(),
}))

vi.mock('../api/client', () => ({
  api: apiMock,
  errText: (e: unknown) => String((e as Error)?.message ?? e),
  ApiError: class ApiError extends Error {
    status: number
    constructor(status: number, message: string) {
      super(message)
      this.status = status
    }
  },
}))
vi.mock('../auth', () => ({ useAuth: () => ({ name: 'Alice', user: 'alice', admin: false }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}))
vi.mock('../components/Markdown', () => ({ default: ({ md }: { md: string }) => <div>{md}</div> }))
vi.mock('../lib/batchUi', () => ({ difyModeTag: () => <span>agent-chat</span> }))

function renderPage() {
  return render(
    <App>
      <MemoryRouter>
        <ChatPage />
      </MemoryRouter>
    </App>,
  )
}

describe('ChatPage responsive layout', () => {
  beforeEach(() => {
    HTMLElement.prototype.scrollTo = vi.fn()
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.del.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url === '/api/chat/targets') return Promise.resolve({ targets: [{ id: 1, name: 'Technical Analysis', mode: 'agent-chat' }] })
      if (url === '/api/chat/config') return Promise.resolve({ stream: true, reconcile_seconds: 300 })
      if (url === '/api/chat/conversations?target_id=1') return Promise.resolve({ conversations: [] })
      if (url === '/api/chat/targets/1/intro') return Promise.resolve({ opening: 'How can I help?' })
      return Promise.resolve({})
    })
  })

  it('uses the compact focus layout and an inline composer on mobile', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: false } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderPage()

    await screen.findByText('Technical Analysis')
    expect(container.querySelector('.rp-chat-page--compact')).not.toBeNull()
    expect(container.querySelector('.rp-chat-panel--compact')).not.toBeNull()
    expect(container.querySelector('.rp-chat-composer--compact')).not.toBeNull()

    const conversations = screen.getByRole('button', { name: 'chat.conversations' })
    expect(conversations.textContent).toBe('')
    expect(screen.getByRole('button', { name: 'nav.home' })).toBeTruthy()
    expect(screen.queryByText('chat.enterHint')).toBeNull()
  })

  // The whole page used to be replaced by "no chat or agent apps yet" for the flight of the
  // first GET — advice about Manage → Targets, given before the server had said anything.
  it('says it is loading instead of declaring the deployment has no assistants', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    apiMock.get.mockImplementation((url: string) =>
      url === '/api/chat/targets' ? new Promise(() => {}) : Promise.resolve({}),
    )
    renderPage()
    expect(await screen.findByText('common.loading')).toBeTruthy()
    expect(screen.queryByText('chat.noTargets')).toBeNull()
  })

  it('reports a failed target list rather than an empty one', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    apiMock.get.mockImplementation((url: string) =>
      url === '/api/chat/targets' ? Promise.reject(new Error('boom')) : Promise.resolve({}),
    )
    renderPage()
    expect(await screen.findByText('common.loadFailedContent')).toBeTruthy()
    expect(screen.getByText('common.retry')).toBeTruthy()
    expect(screen.queryByText('chat.noTargets')).toBeNull()
  })

  it('keeps the full desktop controls and composer hint', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true } as ReturnType<typeof Grid.useBreakpoint>)
    const { container } = renderPage()

    await waitFor(() => expect(container.querySelector('.rp-chat-page--compact')).toBeNull())
    expect(screen.queryByRole('button', { name: 'nav.home' })).toBeNull()
    expect(screen.getByText('chat.enterHint')).toBeTruthy()
  })
})
