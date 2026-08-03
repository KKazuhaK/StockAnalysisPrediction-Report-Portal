import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
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

describe('SSOPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.post.mockReset()
    apiMock.get.mockImplementation((url: string) => {
      if (url === '/api/admin/sso/providers') {
        return Promise.resolve({ providers: [], public_url: 'https://p.example', sp_defaults: SP_DEFAULTS })
      }
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
