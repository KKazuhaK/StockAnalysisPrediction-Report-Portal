import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import ManageLayout from './ManageLayout'

const navigate = vi.fn()

// The layout reads the active key off the path and navigates on menu click.
vi.mock('react-router-dom', () => ({
  useNavigate: () => navigate,
  useLocation: () => ({ pathname: '/manage/links' }),
  Outlet: () => null,
}))

// Echo the i18n key so menu entries are findable by their key.
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))

describe('ManageLayout — grouped nav', () => {
  beforeEach(() => navigate.mockReset())

  it('renders section group headers, not a flat tab bar', () => {
    render(<ManageLayout />)
    for (const header of [
      'nav.group.site',
      'nav.group.content',
      'nav.group.access',
      'nav.group.batch',
      'nav.group.integrations',
      'nav.group.maintenance',
    ]) {
      expect(screen.getByText(header)).toBeTruthy()
    }
  })

  it('exposes the pages that used to be buried under Settings as top-level items', () => {
    render(<ManageLayout />)
    for (const leaf of ['settings.general', 'nav.announcement', 'settings.tokens', 'settings.apidoc', 'settings.legacyTab']) {
      expect(screen.getByText(leaf)).toBeTruthy()
    }
  })

  it('navigates to the sub-route when a menu item is clicked', () => {
    render(<ManageLayout />)
    fireEvent.click(screen.getByText('nav.webhooks'))
    expect(navigate).toHaveBeenCalledWith('/manage/webhooks')
  })
})
