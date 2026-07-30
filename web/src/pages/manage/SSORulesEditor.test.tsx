import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import SSORulesEditor from './SSORulesEditor'
import type { SSOProviderAdmin, UserGroupRow } from '../../api/types'

// The rules editor is the missing half of a feature that already had a complete backend: the engine
// resolves role AND organizational unit for every federated login, and with no UI the rule list was
// always empty, so every login fell through to the provider defaults. These tests pin the two things
// that make the page worth having — it renders what is stored, and Save sends the whole ordered
// array (the array IS the order, first match wins).

const saved: unknown[] = []

vi.mock('../../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url === '/api/admin/sso/rules')
        return Promise.resolve({
          rules: [
            {
              id: 1,
              provider_id: 0,
              ord: 0,
              enabled: true,
              attr: 'groups',
              value: 'staff',
              target_role: 'operator',
              target_group: 0,
              keep_on_miss: false,
              ci: true,
              note: '',
            },
            {
              id: 2,
              provider_id: 0,
              ord: 1,
              enabled: true,
              attr: 'groups',
              value: 'staff',
              target_role: 'admin',
              target_group: 0,
              keep_on_miss: false,
              ci: true,
              note: '',
            },
          ],
          shadowed: [2],
        })
      return Promise.resolve({})
    },
    put: (_url: string, body: unknown) => {
      saved.push(body)
      return Promise.resolve({})
    },
    post: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

const providers: SSOProviderAdmin[] = [
  { id: 3, kind: 'oidc', slug: 'idp', name: 'Corp IdP' } as SSOProviderAdmin,
]
const groups: UserGroupRow[] = [{ id: 5, name: 'Clients' } as UserGroupRow]
const roles = [
  { code: 'user', name: 'User' },
  { code: 'operator', name: 'Operator' },
]

const mount = () =>
  render(
    <App>
      <SSORulesEditor providers={providers} groups={groups} roles={roles} />
    </App>,
  )

describe('SSORulesEditor', () => {
  it('renders the stored rules and flags the one that can never win', async () => {
    mount()
    // Both rules deliberately carry the same attribute/value — that is what makes the second one
    // unreachable — so there are two inputs showing it.
    expect((await screen.findAllByDisplayValue('staff')).length).toBe(2)
    // The second rule sits behind the first on the same attribute and value, so the server marked
    // it unreachable — an admin must see that, or it reads as a granted permission.
    expect(await screen.findByText('sso.rules.shadowed')).toBeTruthy()
  })

  it('saves the whole ordered array, so a removed rule stops granting anything', async () => {
    saved.length = 0
    const { container } = mount()
    await screen.findAllByDisplayValue('staff')

    // Delete the first rule, which is also what proves order is carried rather than reconstructed.
    const del = container.querySelectorAll('button.ant-btn-dangerous')
    expect(del.length).toBe(2)
    fireEvent.click(del[0])

    fireEvent.click(await screen.findByText('common.save'))
    await waitFor(() => expect(saved.length).toBe(1))
    const body = saved[0] as { rules: Array<{ target_role: string }> }
    expect(body.rules.length).toBe(1)
    expect(body.rules[0].target_role).toBe('admin')
  })

  it('says so when no provider exists, because then no rule can ever fire', async () => {
    render(
      <App>
        <SSORulesEditor providers={[]} groups={groups} roles={roles} />
      </App>,
    )
    expect(await screen.findByText('sso.rules.noProviders')).toBeTruthy()
  })
})
