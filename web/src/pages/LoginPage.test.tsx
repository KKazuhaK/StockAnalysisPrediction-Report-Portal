import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import LoginPage from './LoginPage'

// vi.mock is hoisted above the file body, so the mocks it closes over must be hoisted too.
const { apiMock, authMock, webauthnMock, navMock } = vi.hoisted(() => ({
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
  navMock: { hardNavigate: vi.fn() },
}))

// ApiError carries a code here, because the forgot dialog branches on one: a captcha refusal is
// the single failure it may report, and every other one still has to read as "sent". Hoisted with
// the mocks, since vi.mock's factory runs before the file body.
const { FakeApiError } = vi.hoisted(() => ({
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
vi.mock('../auth', () => ({ useAuth: () => authMock }))
vi.mock('../lib/webauthn', () => webauthnMock)
vi.mock('../lib/hardNavigate', () => navMock)
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

// The four login modes. The server resolves them (login_mode.go) and hands the page `login_mode`
// plus two booleans, so these tests pin that the page renders off those and re-derives nothing.
describe('LoginPage login modes', () => {
  const ACME = { slug: 'acme', kind: 'oidc', name: 'Acme' }

  const mockMode = (mode: string, local: boolean, sso: boolean) => {
    apiMock.get.mockReset()
    apiMock.get.mockImplementation((url: string) =>
      url === '/api/sso/providers'
        ? Promise.resolve({ providers: [ACME], login_mode: mode, local, sso })
        : Promise.resolve({ enabled: false }),
    )
  }

  it('dual shows the password form and the provider together', async () => {
    mockMode('dual', true, true)
    renderLogin()
    expect(await screen.findByText('login.ssoWith:{"name":"Acme"}')).toBeTruthy()
    expect(screen.getByText('login.submit')).toBeTruthy()
  })

  it('sso_first hides the password form behind one deliberate click', async () => {
    mockMode('sso_first', true, true)
    renderLogin()
    expect(await screen.findByText('login.ssoWith:{"name":"Acme"}')).toBeTruthy()
    expect(screen.queryByText('login.submit')).toBeNull()

    fireEvent.click(screen.getByText('login.usePassword'))
    expect(await screen.findByText('login.submit')).toBeTruthy()
  })

  it('local_only hides the provider even though one is configured', async () => {
    mockMode('local_only', true, false)
    renderLogin()
    expect(await screen.findByText('login.submit')).toBeTruthy()
    expect(screen.queryByText('login.ssoWith:{"name":"Acme"}')).toBeNull()
    expect(screen.queryByText('login.ssoDivider')).toBeNull()
  })

  it('sso_redirect sends the browser to the provider', async () => {
    navMock.hardNavigate.mockReset()
    mockMode('sso_redirect', false, true)
    renderLogin()
    await waitFor(() => expect(navMock.hardNavigate).toHaveBeenCalledWith('/api/auth/oidc/acme/start'))
  })

  // The escape hatch. It only declines the auto-redirect — whether a password is ACCEPTED is the
  // server's call, and admins stay exempt there — but without it a misconfigured IdP would make the
  // page itself unreachable, before anyone can prove who they are.
  // The escape has to actually escape. Skipping the redirect onto a page whose only control is the
  // provider that just failed is a dead end, not a break-glass path.
  // An already-signed-in visitor must be sent home, not thrown at the identity provider. The
  // redirect used to fire from the fetch callback, which knows nothing about the session — and
  // could even fire after the auth check had already unmounted this page.
  it('sso_redirect does not redirect a visitor who is already signed in', async () => {
    navMock.hardNavigate.mockReset()
    authMock.user = 'alice'
    try {
      mockMode('sso_redirect', false, true)
      renderLogin()
      // Wait for the fetch the redirect decision hangs off, so this asserts "it decided not to go"
      // rather than "50ms elapsed and nothing had happened yet".
      await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/sso/providers'))
      expect(navMock.hardNavigate).not.toHaveBeenCalled()
    } finally {
      authMock.user = null
    }
  })

  it('sso_redirect with ?local=1 reveals the password form', async () => {
    navMock.hardNavigate.mockReset()
    window.history.replaceState({}, '', '/login?local=1')
    try {
      mockMode('sso_redirect', false, true)
      renderLogin()
      expect(await screen.findByText('login.submit')).toBeTruthy()
      expect(navMock.hardNavigate).not.toHaveBeenCalled()
    } finally {
      window.history.replaceState({}, '', '/')
    }
  })

  // A failed handshake lands back here. If that did not count as a bypass the page would redirect
  // straight back into the IdP that just refused it — a loop no browser breaks, because it is
  // JS-driven rather than a server redirect chain.
  it('sso_redirect after a failed handshake stops, explains, and offers the password form', async () => {
    navMock.hardNavigate.mockReset()
    window.history.replaceState({}, '', '/login?sso_error=not_provisioned')
    try {
      mockMode('sso_redirect', false, true)
      renderLogin()
      expect(await screen.findByText(/login\.ssoFailed/)).toBeTruthy()
      expect(screen.getByText('login.submit')).toBeTruthy()
      expect(navMock.hardNavigate).not.toHaveBeenCalled()
    } finally {
      window.history.replaceState({}, '', '/')
    }
  })

  it('sso_redirect with ?local=1 stays on the page', async () => {
    navMock.hardNavigate.mockReset()
    // history.replaceState changes location.search without navigating, so no jsdom global is
    // replaced — overwriting window.location destabilised unrelated test files.
    window.history.replaceState({}, '', '/login?local=1')
    try {
      mockMode('sso_redirect', false, true)
      renderLogin()
      expect(await screen.findByText('login.ssoWith:{"name":"Acme"}')).toBeTruthy()
      expect(navMock.hardNavigate).not.toHaveBeenCalled()
    } finally {
      window.history.replaceState({}, '', '/')
    }
  })
})
