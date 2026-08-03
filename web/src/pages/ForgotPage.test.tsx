import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import ForgotPage from './ForgotPage'

// The captcha toggle for this form was reported as doing nothing. It was working the whole time:
// the server refuses a proofless request with 400 captcha_failed. The form never rendered a
// captcha, never sent one, and caught every error to report "sent" regardless — so the switch was
// enforced and invisible, and the reset mail silently never left.

const { apiMock, FakeApiError } = vi.hoisted(() => ({
  apiMock: { get: vi.fn(), post: vi.fn() },
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
  errText: (e: unknown, t: (k: string) => string) =>
    e instanceof FakeApiError && e.code ? t(`err.${e.code}`) : t('common.error'),
}))
vi.mock('../site', () => ({ useSite: () => ({ title: 'Portal' }), SiteLogo: () => null }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const renderPage = () =>
  render(
    <MemoryRouter>
      <App>
        <ForgotPage />
      </App>
    </MemoryRouter>,
  )

describe('ForgotPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockResolvedValue({
      required: true,
      provider: 'image',
      captcha_id: 'c1',
      image: 'data:image/png;base64,x',
    })
    apiMock.post.mockResolvedValue({ ok: true })
  })

  const fill = async (answer: string) => {
    fireEvent.change(await screen.findByPlaceholderText('login.forgotAccount'), { target: { value: 'kazuha' } })
    fireEvent.change(await screen.findByPlaceholderText('captcha.placeholder'), { target: { value: answer } })
    fireEvent.click(screen.getByText('login.forgotSend'))
  }

  it('asks for the captcha the server says it needs', async () => {
    renderPage()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith(expect.stringContaining('ctx=forgot')))
    expect(await screen.findByPlaceholderText('captcha.placeholder')).toBeTruthy()
  })

  it('sends the answer with the request', async () => {
    renderPage()
    await fill('4271')
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/password/forgot', {
        account: 'kazuha',
        captcha_id: 'c1',
        captcha_answer: '4271',
      }),
    )
  })

  // "Always say sent" exists so nobody can enumerate accounts. A wrong captcha is not an account
  // fact — the server decides it before looking anything up — so reporting it leaks nothing, and
  // reporting success instead is a lie that loses mail.
  it('says the captcha was wrong instead of claiming the mail was sent', async () => {
    renderPage()
    apiMock.post.mockRejectedValueOnce(new FakeApiError(400, 'captcha is required or incorrect', 'captcha_failed'))
    await fill('nope')
    expect(await screen.findByText('err.captcha_failed')).toBeTruthy()
    expect(screen.queryByText('login.forgotSent')).toBeNull()
    // Re-armed, because a challenge is consumed on use.
    await waitFor(() => expect(apiMock.get.mock.calls.length).toBeGreaterThan(1))
  })

  it('still reports sent when the failure could reveal whether the account exists', async () => {
    renderPage()
    apiMock.post.mockRejectedValueOnce(new FakeApiError(500, 'boom'))
    await fill('4271')
    expect(await screen.findByText('login.forgotSent')).toBeTruthy()
  })
})
