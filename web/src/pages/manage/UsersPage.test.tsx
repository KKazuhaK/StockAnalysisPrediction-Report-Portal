import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { App } from 'antd'
import UsersPage from './UsersPage'

// Groups management used to live in a Drawer opened from a toolbar button; it is now
// the second sub-tab. Both tabs must render off a single load.
vi.mock('../../api/client', () => ({
  api: {
    get: () => Promise.resolve({ users: [], roles: [], groups: [], me: '' }),
    post: () => Promise.resolve({}),
    put: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('UsersPage — accounts / groups sub-tabs', () => {
  it('renders both sub-tabs', async () => {
    render(
      <App>
        <UsersPage />
      </App>,
    )
    expect(await screen.findByText('users.tabAccounts')).toBeTruthy()
    expect(await screen.findByText('users.tabGroups')).toBeTruthy()
  })

  it('offers an account-validity field in the add-user form', async () => {
    render(
      <App>
        <UsersPage />
      </App>,
    )
    fireEvent.click(await screen.findByText('users.add'))
    // The add/edit modal must carry the account expiry (有效期) control (ADR 0022 R4). Assert on the
    // form-only hint so it can't be satisfied by the same-labelled table column header.
    expect(await screen.findByText('users.expiresHint')).toBeTruthy()
  })
})
