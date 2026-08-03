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
      <SSOSetupGuide kind="saml" values={SAML} {...props} />
    </App>,
  )

describe('SSOSetupGuide', () => {
  it('pairs each portal value with the IdP box it belongs in', () => {
    mount()
    fireEvent.click(screen.getByText('sso.guide.title'))
    const text = document.body.textContent ?? ''
    expect(text).toContain(SAML.acs)
    expect(text).toContain('Reply URL (Assertion Consumer Service URL)') // what Entra calls it
  })

  // The same value, a different label, in every product — which is the entire reason a copyable
  // field alone did not answer "填什么".
  it('renames the IdP fields when the vendor changes, without changing the values', () => {
    mount()
    fireEvent.click(screen.getByText('sso.guide.title'))
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
    fireEvent.click(screen.getByText('sso.guide.title'))
    expect(screen.getByText(/sso\.guide\.saml\.metadataShortcut/)).toBeTruthy()
  })

  it('shows the OIDC redirect URI and none of the SAML steps', () => {
    mount({ kind: 'oidc', values: { redirect: 'https://p.example.com/api/auth/oidc/oidc/callback' } })
    fireEvent.click(screen.getByText('sso.guide.title'))
    const text = document.body.textContent ?? ''
    expect(text).toContain('https://p.example.com/api/auth/oidc/oidc/callback')
    expect(text).toContain('Redirect URI (Web)')
    expect(text).not.toContain('Assertion Consumer Service')
  })

  // Without a public URL the server derives bare paths. Handing an admin "/api/auth/saml/saml/acs"
  // to paste into Entra is worse than handing them nothing: it looks like a value.
  it('withholds the values, rather than offering a relative path, when there is no public URL', () => {
    mount({ values: { entityId: '/api/auth/saml/saml/metadata', acs: '/api/auth/saml/saml/acs' } })
    fireEvent.click(screen.getByText('sso.guide.title'))
    expect(document.body.textContent).not.toContain('/api/auth/saml/saml/acs')
    expect(screen.getByText(/sso\.guide\.needPublicUrl/)).toBeTruthy()
  })

  // Folded until asked for. It is reference material an admin needs once; the settings underneath
  // are what they come back to, and those should not start a screen further down the page.
  it('starts folded', () => {
    // Exact, not a prefix match: the step's own body key is 'sso.guide.saml.step1.<vendor>'.
    mount()
    expect(screen.queryByText('sso.guide.saml.step1')).toBeNull()
    fireEvent.click(screen.getByText('sso.guide.title'))
    expect(screen.getByText('sso.guide.saml.step1')).toBeTruthy()
  })

  // An empty box with a copy button beside it is the same lie as a relative path: it looks like a
  // value. This is what a portal that had never saved a provider actually showed.
  it('withholds the values when the server had none to give', () => {
    mount({ values: { entityId: '', acs: '' } })
    fireEvent.click(screen.getByText('sso.guide.title'))
    expect(screen.getByText(/sso\.guide\.needPublicUrl/)).toBeTruthy()
  })
})
