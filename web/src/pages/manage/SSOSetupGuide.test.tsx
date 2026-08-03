import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { App } from 'antd'
import SSOSetupGuide from './SSOSetupGuide'

// The complaint this answers: an admin opens the SSO page, sees "SP Entity ID" and a box for the
// IdP metadata URL, and has no idea which of Entra's boxes each one belongs in. Copyable values
// were already on the page; what was missing is the sentence pairing each value with the field it
// goes into — and that field has a DIFFERENT NAME in every product.

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
  }),
}))

const SAML = { entityId: 'https://p.example.com/api/auth/saml/saml/metadata', acs: 'https://p.example.com/api/auth/saml/saml/acs' }

const mount = (props: Partial<React.ComponentProps<typeof SSOSetupGuide>> = {}) =>
  render(
    <App>
      <SSOSetupGuide kind="saml" values={SAML} configured={false} {...props} />
    </App>,
  )

describe('SSOSetupGuide', () => {
  it('pairs each portal value with the IdP box it belongs in', () => {
    mount()
    const text = document.body.textContent ?? ''
    expect(text).toContain(SAML.acs)
    expect(text).toContain('Reply URL (Assertion Consumer Service URL)') // what Entra calls it
  })

  // The same value, a different label, in every product — which is the entire reason a copyable
  // field alone did not answer "填什么".
  it('renames the IdP fields when the vendor changes, without changing the values', () => {
    mount()
    fireEvent.mouseDown(screen.getByRole('combobox'))
    fireEvent.click(screen.getByTitle('Okta'))
    const text = document.body.textContent ?? ''
    expect(text).toContain('Single sign-on URL') // Okta's name for the ACS URL
    expect(text).not.toContain('Reply URL (Assertion Consumer Service URL)')
    expect(text).toContain(SAML.acs) // the value is a property of the portal, not of the IdP
  })

  it('tells a SAML admin that the Entity ID doubles as the metadata document', () => {
    // Entra offers "Upload metadata file", which fills three boxes at once — but only if you know
    // the URL you were told to paste as an Entity ID is also that file.
    mount()
    expect(screen.getByText(/sso\.guide\.saml\.metadataShortcut/)).toBeTruthy()
  })

  it('shows the OIDC redirect URI and none of the SAML steps', () => {
    mount({ kind: 'oidc', values: { redirect: 'https://p.example.com/api/auth/oidc/oidc/callback' } })
    const text = document.body.textContent ?? ''
    expect(text).toContain('https://p.example.com/api/auth/oidc/oidc/callback')
    expect(text).toContain('Redirect URI (Web)')
    expect(text).not.toContain('Assertion Consumer Service')
  })

  // Without a public URL the server derives bare paths. Handing an admin "/api/auth/saml/saml/acs"
  // to paste into Entra is worse than handing them nothing: it looks like a value.
  it('withholds the values, rather than offering a relative path, when there is no public URL', () => {
    mount({ values: { entityId: '/api/auth/saml/saml/metadata', acs: '/api/auth/saml/saml/acs' } })
    expect(document.body.textContent).not.toContain('/api/auth/saml/saml/acs')
    expect(screen.getByText(/sso\.guide\.needPublicUrl/)).toBeTruthy()
  })

  it('starts folded once the provider is already working, and open while it is not', () => {
    // Exact, not a prefix match: the step's own body key is 'sso.guide.saml.step1.<vendor>'.
    const step1 = (): HTMLElement | null => screen.queryByText('sso.guide.saml.step1')
    const { unmount } = mount({ configured: true })
    expect(step1()).toBeNull()
    unmount()
    mount({ configured: false })
    expect(step1()).toBeTruthy()
  })
})
