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

// The 找回密码 dialog and the captcha toggle that governs it.
//
// The toggle was working the whole time — the server refuses a proofless request with 400
// captcha_failed. The dialog never rendered a captcha and never sent one, and it swallowed every
// error to avoid leaking which accounts exist, so it reported "sent" over a refusal. From the
// admin's chair that is indistinguishable from a switch that does nothing, and the reset mail they
// were waiting for never left.
describe('LoginPage — the forgot-password captcha', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      // Exactly the configuration reported: the 找回密码 form gated, the login form not. It also
      // keeps one captcha on screen, so a query for it cannot match the login form's by accident.
      if (url.startsWith('/api/captcha')) {
        return Promise.resolve(
          url.includes('ctx=forgot')
            ? { required: true, provider: 'image', captcha_id: 'c1', image: 'data:image/png;base64,x' }
            : { required: false, provider: 'image' },
        )
      }
      if (url === '/api/sso/providers') return Promise.resolve({ providers: [], local: true, sso: false })
      return Promise.resolve({})
    })
    apiMock.post.mockResolvedValue({ ok: true })
  })

  const openForgot = async () => {
    renderLogin()
    fireEvent.click(await screen.findByText('login.forgot'))
    return screen.findByPlaceholderText('login.forgotAccount')
  }

  it('asks for the captcha the server says it needs', async () => {
    await openForgot()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith(expect.stringContaining('ctx=forgot')))
    expect(await screen.findByPlaceholderText('captcha.placeholder')).toBeTruthy()
  })

  it('sends the answer with the request', async () => {
    const acct = await openForgot()
    fireEvent.change(acct, { target: { value: 'kazuha' } })
    fireEvent.change(await screen.findByPlaceholderText('captcha.placeholder'), { target: { value: '4271' } })
    fireEvent.click(screen.getByText('login.forgotSend'))
    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/password/forgot', {
        account: 'kazuha',
        captcha_id: 'c1',
        captcha_answer: '4271',
      }),
    )
  })

  // "Always say sent" exists so nobody can enumerate accounts. A wrong captcha is not an account
  // fact, so reporting it leaks nothing — and reporting success instead is a lie that loses mail.
  it('says the captcha was wrong instead of claiming the mail was sent', async () => {
    const acct = await openForgot()
    fireEvent.change(acct, { target: { value: 'kazuha' } })
    fireEvent.change(await screen.findByPlaceholderText('captcha.placeholder'), { target: { value: 'nope' } })
    apiMock.post.mockRejectedValueOnce(new FakeApiError(400, 'captcha is required or incorrect', 'captcha_failed'))
    fireEvent.click(screen.getByText('login.forgotSend'))

    expect(await screen.findByText('err.captcha_failed')).toBeTruthy()
    expect(screen.queryByText('login.forgotSent')).toBeNull()
  })

  // Any other refusal still reports "sent": that one IS account-dependent.
  it('still reports sent when the failure could reveal whether the account exists', async () => {
    const acct = await openForgot()
    fireEvent.change(acct, { target: { value: 'kazuha' } })
    fireEvent.change(await screen.findByPlaceholderText('captcha.placeholder'), { target: { value: '4271' } })
    apiMock.post.mockRejectedValueOnce(new FakeApiError(500, 'boom'))
    fireEvent.click(screen.getByText('login.forgotSend'))
    expect(await screen.findByText('login.forgotSent')).toBeTruthy()
  })
})
