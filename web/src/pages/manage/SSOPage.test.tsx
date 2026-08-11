import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App, Grid } from 'antd'
import SSOPage from './SSOPage'

// Setting up SSO is a long form filled in one sitting, and fetching the IdP metadata happens in the
// MIDDLE of it. Anything that throws away what has been typed so far turns a five-minute task into
// a guessing game about which fields survive.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }))
vi.mock('../../api/client', () => ({
  api: apiMock,
  ApiError: class extends Error {},
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))
vi.mock('./SSORulesEditor', () => ({ default: () => <div>rules</div> }))

const SP_DEFAULTS = {
  saml: {
    sp_entity_id: 'https://p.example/api/auth/saml/saml/metadata',
    sp_acs_url: 'https://p.example/api/auth/saml/saml/acs',
  },
  oidc: { redirect_url: 'https://p.example/api/auth/oidc/oidc/callback' },
}

// What the server returns after the fetch created its draft: the metadata landed, and NOTHING the
// admin typed is in it, because the fetch endpoint only ever knew about the URL.
const DRAFT_AFTER_FETCH = {
  id: 1,
  kind: 'saml',
  slug: 'saml',
  name: '',
  enabled: false,
  has_idp_metadata: true,
  idp_entity_id: 'https://sts.windows.net/e72914d3/',
  idp_metadata_url: 'https://login.microsoftonline.com/x/federationmetadata.xml',
}

// jsdom's matchMedia never matches, so antd reports every breakpoint as absent and the page would
// render its phone layout under test. Say which one is being tested rather than inheriting it.
const screenWidth = (wide: boolean) =>
  vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: wide, lg: wide } as ReturnType<typeof Grid.useBreakpoint>)

describe('SSOPage', () => {
  beforeEach(() => {
    screenWidth(true)
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url === '/api/admin/sso/providers') {
        return Promise.resolve({ providers: [], public_url: 'https://p.example', sp_defaults: SP_DEFAULTS })
      }
      if (url.includes('/last-seen')) return Promise.resolve({ seen: false })
      return Promise.resolve({ groups: [], roles: [] })
    })
    apiMock.post.mockResolvedValue({ ok: true, entity_id: 'https://sts.windows.net/e72914d3/' })
  })

  const typeName = async (value: string) => {
    const input = await screen.findByPlaceholderText('Corporate SSO')
    fireEvent.change(input, { target: { value } })
    return input as HTMLInputElement
  }

  it('keeps what the admin has typed when the metadata is fetched', async () => {
    render(
      <App>
        <SSOPage />
      </App>,
    )
    const name = await typeName('Kazuha Hub SSO')
    const url = screen.getByPlaceholderText(/federationmetadata/)
    fireEvent.change(url, { target: { value: 'https://login.microsoftonline.com/x/federationmetadata.xml' } })

    // The server answers, and the reload it used to trigger returned a nameless draft.
    apiMock.get.mockImplementation((u: string) =>
      u === '/api/admin/sso/providers'
        ? Promise.resolve({ providers: [DRAFT_AFTER_FETCH], public_url: 'https://p.example', sp_defaults: SP_DEFAULTS })
        : Promise.resolve({ groups: [], roles: [] }),
    )
    fireEvent.click(screen.getByText('sso.fetchMetadata'))

    await waitFor(() => expect(apiMock.post).toHaveBeenCalled())
    // The name the admin typed is still there. It is the field they noticed; every other unsaved
    // one went the same way.
    await waitFor(() => expect(name.value).toBe('Kazuha Hub SSO'))
  })

  it('sends the URL currently in the form, and the kind that lets the server make a draft', async () => {
    render(
      <App>
        <SSOPage />
      </App>,
    )
    await typeName('Kazuha Hub SSO')
    const url = await screen.findByPlaceholderText(/federationmetadata/)
    fireEvent.change(url, { target: { value: 'https://login.microsoftonline.com/x/federationmetadata.xml' } })
    fireEvent.click(screen.getByText('sso.fetchMetadata'))

    await waitFor(() =>
      expect(apiMock.post).toHaveBeenCalledWith('/api/admin/sso/providers/saml/metadata', {
        kind: 'saml',
        idp_metadata_url: 'https://login.microsoftonline.com/x/federationmetadata.xml',
      }),
    )
  })

  // The endpoint for this shipped with SSO and nothing ever called it, so the guesswork it was
  // written to end went on — and cost a real sign-in, whose failure message pointed at accounts
  // rather than at the blank claim field that caused it.
  it('shows the claim names the identity provider actually sent', async () => {
    apiMock.get.mockImplementation((url: string) => {
      if (url === '/api/admin/sso/providers') {
        return Promise.resolve({ providers: [], public_url: 'https://p.example', sp_defaults: SP_DEFAULTS })
      }
      if (url.includes('/last-seen')) {
        return Promise.resolve({
          seen: true,
          claims: [
            { name: 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress', preview: 'me@kazuha.org' },
          ],
        })
      }
      return Promise.resolve({ groups: [], roles: [] })
    })
    render(
      <App>
        <SSOPage />
      </App>,
    )
    expect(
      await screen.findByText('http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress'),
    ).toBeTruthy()
  })

  // A claim name is a 70-character URN and its value is another one. Two columns of that on a
  // phone left the name column about two characters wide and pushed the value off the card, so
  // below md the pair stacks — same names, no table.
  it('stacks the claims on a phone instead of tabling them', async () => {
    screenWidth(false)
    apiMock.get.mockImplementation((url: string) => {
      if (url === '/api/admin/sso/providers') {
        return Promise.resolve({ providers: [], public_url: 'https://p.example', sp_defaults: SP_DEFAULTS })
      }
      if (url.includes('/last-seen')) {
        return Promise.resolve({
          seen: true,
          claims: [{ name: 'http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress', preview: 'me@kazuha.org' }],
        })
      }
      return Promise.resolve({ groups: [], roles: [] })
    })
    const { container } = render(
      <App>
        <SSOPage />
      </App>,
    )
    expect(
      await screen.findByText('http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress'),
    ).toBeTruthy()
    // The value travels with the name, which is the whole point of showing it.
    expect(screen.getByText('me@kazuha.org')).toBeTruthy()
    expect(container.querySelector('.rp-claims-item')).toBeTruthy()
    expect(container.querySelector('.rp-claims-table')).toBeNull()
  })

  it('says how to make the claim names appear when nothing has signed in yet', async () => {
    render(
      <App>
        <SSOPage />
      </App>,
    )
    expect(await screen.findByText('sso.claimsNoneYet')).toBeTruthy()
  })

  it('confirms the metadata arrived without asking the server again', async () => {
    render(
      <App>
        <SSOPage />
      </App>,
    )
    const url = await screen.findByPlaceholderText(/federationmetadata/)
    fireEvent.change(url, { target: { value: 'https://login.microsoftonline.com/x/federationmetadata.xml' } })
    expect(screen.getByText('sso.metadataMissing')).toBeTruthy()

    fireEvent.click(screen.getByText('sso.fetchMetadata'))
    // The badge turns green from the fetch's own answer — reloading is what discarded the form.
    await waitFor(() => expect(screen.getByText('https://sts.windows.net/e72914d3/')).toBeTruthy())
  })
})
