import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import LoginPage from './LoginPage'

// vi.mock is hoisted above the file body, so the mocks it closes over must be hoisted too.
const { apiMock, authMock } = vi.hoisted(() => ({
  apiMock: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  authMock: { user: null as unknown, loading: false, login: vi.fn(), loginTOTP: vi.fn(), logout: vi.fn() },
}))

vi.mock('../api/client', () => ({ api: apiMock, ApiError: class extends Error {} }))
vi.mock('../auth', () => ({ useAuth: () => authMock }))
vi.mock('../prefs', () => ({
  usePrefs: () => ({ mode: 'light', setMode: vi.fn(), lang: 'en-US', setLang: vi.fn(), langs: [] }),
}))
vi.mock('../site', () => ({ useSite: () => ({ title: 'Portal' }), SiteLogo: () => null }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

const renderLogin = () =>
  render(
    <MemoryRouter>
      <App>
        <LoginPage />
      </App>
    </MemoryRouter>,
  )

describe('LoginPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    authMock.login.mockReset()
    authMock.loginTOTP.mockReset()
  })

  it('shows nothing extra when SSO is off', async () => {
    apiMock.get.mockResolvedValue({ providers: [] })
    renderLogin()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/sso/providers'))
    expect(screen.queryByText('login.ssoDivider')).toBeNull()
  })

  it('offers a button per enabled provider', async () => {
    apiMock.get.mockResolvedValue({
      providers: [
        { slug: 'acme', kind: 'oidc', name: 'Acme' },
        { slug: 'corp', kind: 'saml', name: 'Corp' },
      ],
    })
    renderLogin()
    expect(await screen.findByText('login.ssoWith:{"name":"Acme"}')).toBeTruthy()
    expect(await screen.findByText('login.ssoWith:{"name":"Corp"}')).toBeTruthy()
  })

  // The password leg must not be treated as a completed sign-in when 2FA is on.
  it('switches to the code step when the password leg reports 2FA', async () => {
    apiMock.get.mockResolvedValue({ providers: [] })
    authMock.login.mockResolvedValue({ totpToken: 'pending-token' })
    renderLogin()

    fireEvent.change(await screen.findByLabelText('login.username'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('login.password'), { target: { value: 'pw' } })
    fireEvent.click(screen.getByText('login.submit'))

    expect(await screen.findByText('login.totpHint')).toBeTruthy()
    // The password fields are gone, so there is no way to mistake this for a finished login.
    expect(screen.queryByText('login.submit')).toBeNull()
  })

  it('signs in directly when 2FA is not required', async () => {
    apiMock.get.mockResolvedValue({ providers: [] })
    authMock.login.mockResolvedValue({})
    renderLogin()

    fireEvent.change(await screen.findByLabelText('login.username'), { target: { value: 'alice' } })
    fireEvent.change(screen.getByLabelText('login.password'), { target: { value: 'pw' } })
    fireEvent.click(screen.getByText('login.submit'))

    await waitFor(() => expect(authMock.login).toHaveBeenCalledWith('alice', 'pw'))
    expect(screen.queryByText('login.totpHint')).toBeNull()
  })
})
