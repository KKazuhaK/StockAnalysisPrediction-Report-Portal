import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import RegisterPage from './RegisterPage'

// The register form was checked after the same defect was found on the forgot-password one: a
// captcha the admin had switched on, enforced by the server, and never rendered or sent by the
// page — so the toggle looked inert while the request it guarded silently failed. This form was
// already correct. These pin the three things that made it correct, because "already fine" is not
// a property that survives on its own.

const { apiMock, authMock, FakeApiError } = vi.hoisted(() => ({
  apiMock: { get: vi.fn(), post: vi.fn() },
  authMock: { user: null, loading: false },
  FakeApiError: class extends Error {
    status: number
    code?: string
    constructor(status: number, message: string, code?: string) {
      super(message)
      this.status = status
      this.code = code
    }
  },
}))

vi.mock('../api/client', () => ({
  api: apiMock,
  ApiError: FakeApiError,
  errText: (e: unknown, t: (k: string) => string, fb = 'common.error') =>
    e instanceof FakeApiError && e.code ? t(`err.${e.code}`) : t(fb),
}))
vi.mock('../auth', () => ({ useAuth: () => authMock }))
vi.mock('../site', () => ({ useSite: () => ({ title: 'Portal' }), SiteLogo: () => null }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const renderPage = () =>
  render(
    <MemoryRouter>
      <App>
        <RegisterPage />
      </App>
    </MemoryRouter>,
  )

describe('RegisterPage — the captcha it is gated by', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url.startsWith('/api/captcha')) {
        return Promise.resolve({ required: true, provider: 'image', captcha_id: 'c1', image: 'data:image/png;base64,x' })
      }
      return Promise.resolve({ enabled: true, requires_verification: false })
    })
    apiMock.post.mockResolvedValue({ requires_verification: false })
  })

  const fillAndSubmit = async (answer: string) => {
    // By label, not by placeholder: the only placeholder on this form belongs to the captcha.
    fireEvent.change(await screen.findByLabelText('register.email'), {
      target: { value: 'kazuha@corp.example' },
    })
    fireEvent.change(screen.getByLabelText('register.password'), { target: { value: 'a-long-enough-password' } })
    fireEvent.change(await screen.findByPlaceholderText('captcha.placeholder'), { target: { value: answer } })
    fireEvent.click(screen.getByText('register.submit'))
  }

  it('renders the captcha the server says it needs', async () => {
    renderPage()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith(expect.stringContaining('ctx=register')))
    expect(await screen.findByPlaceholderText('captcha.placeholder')).toBeTruthy()
  })

  it('sends the answer with the registration', async () => {
    renderPage()
    await fillAndSubmit('4271')
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith(
        '/api/register',
        expect.objectContaining({ captcha_id: 'c1', captcha_answer: '4271' }),
      ),
    )
  })

  // The failure mode the forgot dialog had: reporting success over a refusal. Here the refusal has
  // to be visible, and the challenge re-armed, because a challenge is consumed on use.
  it('shows a rejected captcha rather than claiming the account was created', async () => {
    renderPage()
    apiMock.post.mockRejectedValueOnce(new FakeApiError(400, 'captcha is required or incorrect', 'captcha_failed'))
    await fillAndSubmit('nope')
    expect(await screen.findByText('err.captcha_failed')).toBeTruthy()
    // Re-armed: the field asked the server for a fresh challenge after the refusal.
    await waitFor(() =>
      expect(apiMock.get.mock.calls.filter((c) => String(c[0]).includes('ctx=register')).length).toBeGreaterThan(1),
    )
  })
})
