import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import LinksPage from './LinksPage'

// Smoke test: the reworked LinksPage (single drag list of group headers + links) must mount
// without crashing and render both a group header and a link. Guards against a render/hook
// regression in the group rework.
vi.mock('../../api/client', () => ({
  api: {
    get: (url: string) => {
      if (url === '/api/admin/links')
        return Promise.resolve({
          links: [{ id: 1, label: 'GitHub', url: 'https://github.com', icon: '', newTab: true, groupId: 5, ord: 0 }],
          groups: [{ id: 5, name: 'External', mode: 'expand', showLabel: true, ord: 0 }],
        })
      return Promise.resolve({ apps: [], targets: [] })
    },
    post: () => Promise.resolve({}),
    put: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))
vi.mock('../../auth', () => ({ useAuth: () => ({ can: () => true }) }))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('LinksPage', () => {
  it('renders group headers and links without crashing', async () => {
    render(
      <App>
        <LinksPage />
      </App>,
    )
    // The link label and the group name (now shown as static text, edited via the pencil→modal)
    // both render.
    expect(await screen.findByText('GitHub')).toBeTruthy()
    expect(screen.getByText('External')).toBeTruthy()
    // The add-group action is present.
    expect(screen.getByText('links.addGroup')).toBeTruthy()
  })
})
