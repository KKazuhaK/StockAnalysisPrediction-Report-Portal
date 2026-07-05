import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import BatchAdminPage from './BatchAdminPage'

// Targets and advanced plugins used to stack as two cards; they are now sub-tabs.
vi.mock('../../api/client', () => ({
  api: {
    get: () => Promise.resolve({ plugins: [], targets: [] }),
    post: () => Promise.resolve({}),
    del: () => Promise.resolve({}),
  },
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

describe('BatchAdminPage — targets / plugins sub-tabs', () => {
  it('renders both sub-tabs', async () => {
    render(
      <App>
        <BatchAdminPage />
      </App>,
    )
    expect(await screen.findByText('batch.admin.targets')).toBeTruthy()
    expect(await screen.findByText('batch.admin.advancedPlugins')).toBeTruthy()
  })
})
