import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router-dom'
import LoginPage from './LoginPage'

// vi.mock is hoisted above the file body, so the mocks it closes over must be hoisted too.
const { apiMock, authMock, webauthnMock } = vi.hoisted(() => ({
  apiMock: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() },
  authMock: {
    user: null as unknown,
    loading: false,
    login: vi.fn(),
    loginTOTP: vi.fn(),
    loginPasskey: vi.fn(),
    logout: vi.fn(),
  },
  webauthnMock: { getCredential: vi.fn(), passkeySupported: vi.fn(() => true) },
}))

vi.mock('../api/client', () => ({ api: apiMock, ApiError: class extends Error {} }))
vi.mock('../auth', () => ({ useAuth: () => authMock }))
vi.mock('../lib/webauthn', () => webauthnMock)
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
    authMock.loginPasskey.mockReset()
    webauthnMock.getCredential.mockReset()
    webauthnMock.passkeySupported.mockReturnValue(true)
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

    // The third argument is the captcha proof, empty when the server is not asking for one.
    await waitFor(() =>
      expect(authMock.login).toHaveBeenCalledWith('alice', 'pw', expect.anything()),
    )
    expect(screen.queryByText('login.totpHint')).toBeNull()
  })
})

// A passkey is the SECOND factor of a password login (ADR 0023), so it is offered at the
// second-factor step — never on the username/password screen, where it would be a passwordless
// entry point the server refuses anyway.
describe('LoginPage passkey second factor', () => {
  beforeEach(() => {
    apiMock.get.mockReset().mockResolvedValue({ providers: [] })
    authMock.login.mockReset()
    authMock.loginPasskey.mockReset()
    webauthnMock.passkeySupported.mockReturnValue(true)
  })

  it('offers a passkey only after the password leg', async () => {
    renderLogin()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    expect(screen.queryByText('login.passkeyUse')).toBeNull()

    authMock.login.mockResolvedValue({ totpToken: 'pending-token' })
    fireEvent.change(document.querySelector('input[autocomplete="username"]')!, { target: { value: 'alice' } })
    fireEvent.change(document.querySelector('input[type="password"]')!, { target: { value: 'pw' } })
    fireEvent.click(screen.getByText('login.submit'))

    expect(await screen.findByText('login.passkeyUse')).toBeTruthy()
  })

  it('completes the login with the pending token from the password leg', async () => {
    authMock.login.mockResolvedValue({ totpToken: 'pending-token' })
    authMock.loginPasskey.mockResolvedValue(undefined)
    renderLogin()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    fireEvent.change(document.querySelector('input[autocomplete="username"]')!, { target: { value: 'alice' } })
    fireEvent.change(document.querySelector('input[type="password"]')!, { target: { value: 'pw' } })
    fireEvent.click(screen.getByText('login.submit'))

    fireEvent.click(await screen.findByText('login.passkeyUse'))
    await waitFor(() => expect(authMock.loginPasskey).toHaveBeenCalledWith('pending-token'))
  })

  it('does not offer a passkey the browser cannot use', async () => {
    webauthnMock.passkeySupported.mockReturnValue(false)
    authMock.login.mockResolvedValue({ totpToken: 'pending-token' })
    renderLogin()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    fireEvent.change(document.querySelector('input[autocomplete="username"]')!, { target: { value: 'alice' } })
    fireEvent.change(document.querySelector('input[type="password"]')!, { target: { value: 'pw' } })
    fireEvent.click(screen.getByText('login.submit'))

    expect(await screen.findByText('login.totpHint')).toBeTruthy()
    expect(screen.queryByText('login.passkeyUse')).toBeNull()
  })
})
