import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import { MemoryRouter } from 'react-router'
import AccountPage from './AccountPage'

// vi.mock is hoisted above the file body, so the mocks it closes over must be hoisted too.
const { apiMock, authMock, webauthnMock } = vi.hoisted(() => ({
  apiMock: { get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn(), stepUp: vi.fn() },
  authMock: {
    user: 'alice',
    name: 'Alice',
    federated: false,
    totpEnabled: false,
    passkeyCount: 0,
    refresh: vi.fn(),
  },
  webauthnMock: { createCredential: vi.fn(), passkeySupported: vi.fn(() => true) },
}))

vi.mock('../api/client', () => ({ api: apiMock, ApiError: class extends Error {} }))
vi.mock('../auth', () => ({ useAuth: () => authMock }))
vi.mock('../lib/webauthn', () => webauthnMock)
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

// The page itself has password inputs (the change-password card), so a proof must be typed into the
// one inside the modal — a document-wide query would silently fill the wrong field.
const modalInput = (selector = 'input') =>
  document.querySelector(`.ant-modal ${selector}`) as HTMLElement

const renderPage = () =>
  render(
    <MemoryRouter>
      <App>
        <AccountPage />
      </App>
    </MemoryRouter>,
  )

describe('AccountPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset().mockResolvedValue({ passkeys: [] })
    apiMock.post.mockReset().mockResolvedValue({ ok: true })
    apiMock.stepUp.mockReset()
    webauthnMock.createCredential.mockReset()
    webauthnMock.passkeySupported.mockReturnValue(true)
    authMock.federated = false
    authMock.totpEnabled = false
    authMock.passkeyCount = 0
  })

  // Under force-SSO the portal has stopped accepting a password at the front door. Drawing a
  // password box here would offer a credential the server will refuse — and would make re-proving
  // identity weaker than signing in. The dialog says why instead of bouncing the user anywhere.
  it('offers the provider instead of a password box when the login mode says so', async () => {
    apiMock.get.mockImplementation((url: string) =>
      url === '/api/account/stepup/policy'
        ? Promise.resolve({ password: false, sso: true, reason: 'sso_required', providers: [{ slug: 'corp', kind: 'oidc', name: 'Corp SSO', icon: '/site-assets/corp.png' }] })
        : Promise.resolve({ passkeys: [] }),
    )
    renderPage()
    fireEvent.click(await screen.findByText('account.totpEnable'))

    await waitFor(() => expect(screen.getByText('account.confirmSSOOnly')).toBeTruthy())
    expect(screen.getByText('account.confirmViaSSO:{"name":"Corp SSO"}')).toBeTruthy()
    // The provider's own configured icon, not a second hard-coded glyph: one provider must not
    // look like two different things depending on which screen you meet it on.
    expect(document.querySelector('.ant-modal img[src="/site-assets/corp.png"]')).toBeTruthy()
    expect(screen.queryByText('account.confirmWithPassword')).toBeNull()
    // Nothing to submit through, so the confirm button must not be sitting there looking usable.
    expect(modalInput('input[type=password]')).toBeNull()
  })

  // Both channels open: the password box stays primary and the provider is an alternative.
  it('offers both when the login mode allows both', async () => {
    apiMock.get.mockImplementation((url: string) =>
      url === '/api/account/stepup/policy'
        ? Promise.resolve({ password: true, sso: true, providers: [{ slug: 'corp', kind: 'oidc', name: 'Corp SSO' }] })
        : Promise.resolve({ passkeys: [] }),
    )
    renderPage()
    fireEvent.click(await screen.findByText('account.totpEnable'))

    await waitFor(() => expect(screen.getByText('account.confirmWithPassword')).toBeTruthy())
    expect(screen.getByText('account.confirmViaSSO:{"name":"Corp SSO"}')).toBeTruthy()
  })

  it('hides the local credential controls for a federated account', async () => {
    authMock.federated = true
    renderPage()
    await waitFor(() => expect(screen.getByText('account.federatedNotice')).toBeTruthy())
    // The password and 2FA cards belong to the IdP, so they must not be offered at all —
    // submitting either would be refused by the server.
    expect(screen.queryByText('account.passwordTitle')).toBeNull()
    expect(screen.queryByText('account.totpTitle')).toBeNull()
    expect(screen.getByText('account.passkeyFederated')).toBeTruthy()
  })

  it('changes the password and asks for the current one', async () => {
    renderPage()
    await waitFor(() => expect(screen.getByText('account.passwordTitle')).toBeTruthy())
    const boxes = document.querySelectorAll('input[type="password"]')
    expect(boxes.length).toBe(3)
    fireEvent.change(boxes[0], { target: { value: 'correct-horse-battery' } })
    fireEvent.change(boxes[1], { target: { value: 'a-brand-new-passphrase' } })
    fireEvent.change(boxes[2], { target: { value: 'a-brand-new-passphrase' } })
    fireEvent.click(screen.getByText('account.changePassword'))

    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/me/password', {
        current: 'correct-horse-battery',
        new: 'a-brand-new-passphrase',
      }),
    )
  })

  it('refuses to submit when the confirmation does not match', async () => {
    renderPage()
    await waitFor(() => expect(screen.getByText('account.passwordTitle')).toBeTruthy())
    const boxes = document.querySelectorAll('input[type="password"]')
    fireEvent.change(boxes[0], { target: { value: 'correct-horse-battery' } })
    fireEvent.change(boxes[1], { target: { value: 'a-brand-new-passphrase' } })
    fireEvent.change(boxes[2], { target: { value: 'a-different-passphrase' } })
    fireEvent.click(screen.getByText('account.changePassword'))

    await waitFor(() => expect(screen.getByText('account.passwordMismatch')).toBeTruthy())
    expect(apiMock.post).not.toHaveBeenCalled()
  })

  // The step-up proof must reach the server as a header, via api.stepUp — never in the URL, and
  // never held on to after the action it was collected for.
  it('sends the step-up proof in a header when enrolling 2FA', async () => {
    apiMock.stepUp.mockResolvedValue({ secret: 'S3CR3T', uri: 'otpauth://x' })
    renderPage()
    await waitFor(() => expect(screen.getByText('account.totpTitle')).toBeTruthy())
    fireEvent.click(screen.getByText('account.totpEnable'))

    await waitFor(() => expect(screen.getByText('account.confirmWithPassword')).toBeTruthy())
    const proof = modalInput('input[type="password"]')
    fireEvent.change(proof, { target: { value: 'correct-horse-battery' } })
    fireEvent.click(screen.getByText('account.confirmOk'))

    await waitFor(() =>
      expect(apiMock.stepUp).toHaveBeenCalledWith('POST', '/api/me/2fa/setup', 'correct-horse-battery'),
    )
    // The secret is shown so it can be added to an authenticator, and nothing is in force yet.
    await waitFor(() => expect(screen.getByText('S3CR3T')).toBeTruthy())
    expect(apiMock.post).not.toHaveBeenCalledWith('/api/me/2fa/enable', expect.anything())
  })

  it('asks for a code rather than a password once 2FA is on', async () => {
    authMock.totpEnabled = true
    renderPage()
    await waitFor(() => expect(screen.getByText('account.totpTitle')).toBeTruthy())
    fireEvent.click(screen.getByText('account.totpDisable'))
    await waitFor(() => expect(screen.getByText('account.confirmWithCode')).toBeTruthy())
  })

  it('registers a passkey through the browser ceremony', async () => {
    apiMock.stepUp.mockResolvedValue({ token: 'tok-1', options: { publicKey: { challenge: 'AA' } } })
    webauthnMock.createCredential.mockResolvedValue({ id: 'cred-1' })
    renderPage()
    await waitFor(() => expect(screen.getByText('account.passkeyTitle')).toBeTruthy())
    fireEvent.click(screen.getByText('account.passkeyAdd'))

    const proof = modalInput('input[type="password"]')
    fireEvent.change(proof, { target: { value: 'correct-horse-battery' } })
    fireEvent.click(screen.getByText('account.confirmOk'))

    await waitFor(() =>
      expect(apiMock.stepUp).toHaveBeenCalledWith(
        'POST',
        '/api/me/passkeys/register/begin',
        'correct-horse-battery',
      ),
    )
    await waitFor(() => expect(webauthnMock.createCredential).toHaveBeenCalledWith({ challenge: 'AA' }))
    await waitFor(() =>
      expect(apiMock.post.mock.calls.some(([url]) => String(url).startsWith('/api/me/passkeys/register/finish'))).toBe(
        true,
      ),
    )
  })

  it('says so instead of offering a passkey the browser cannot create', async () => {
    webauthnMock.passkeySupported.mockReturnValue(false)
    renderPage()
    await waitFor(() => expect(screen.getByText('account.passkeyUnsupported')).toBeTruthy())
    expect(screen.getByText('account.passkeyAdd').closest('button')!.disabled).toBe(true)
  })

  it('lists registered passkeys with their last use', async () => {
    apiMock.get.mockResolvedValue({
      passkeys: [{ id: 4, label: 'YubiKey', last_used_at: '2026-07-01T10:00:00Z' }],
    })
    authMock.passkeyCount = 1
    renderPage()
    await waitFor(() => expect(screen.getByText('YubiKey')).toBeTruthy())
  })
})
