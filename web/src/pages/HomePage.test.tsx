import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import HomePage from './HomePage'
import type { HomeResp } from '../api/types'

// The browse feed's version filter (ADR 0024) is how the reports people wrote by hand become a set
// you can ask for rather than something you find one at a time (ADR 0026). What is worth pinning is
// that it appears exactly when the server says there is more than one written form, that choosing
// one reaches the server, and that a filter already in the URL comes back selected — a filter that
// forgets itself on reload looks like it did not work.

const state: { resp: Partial<HomeResp>; urls: string[] } = { resp: {}, urls: [] }

vi.mock('../api/client', () => ({
  api: {
    get: (u: string) => {
      state.urls.push(u)
      return Promise.resolve(state.resp)
    },
  },
  errText: (_e: unknown, t: (k: string) => string) => t('common.error'),
  qs: (p: Record<string, string>) => {
    const q = new URLSearchParams(Object.entries(p).filter(([, v]) => v !== '')).toString()
    return q ? `?${q}` : ''
  },
}))
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))
vi.mock('../auth', () => ({ useAuth: () => ({ can: () => true }) }))
vi.mock('../site', () => ({ useSite: () => ({ title: 'Portal' }), SiteLogo: () => null }))
vi.mock('../components/Omnibox', () => ({ default: () => null }))
vi.mock('../components/ReportCard', () => ({ default: () => null }))

const base: Partial<HomeResp> = {
  groups: [],
  newTotal: 0,
  oldTotal: 0,
  totalRuns: 0,
  page: 1,
  pages: 1,
  size: 30,
  types: [],
  kinds: [],
  versions: [],
  links: [],
  linkGroups: [],
  kindColors: {},
}

const twoVersions = [
  // The default version arrives with no label of its own — the server falls back to the identifier —
  // so the filter has to name it rather than showing "default" to every reader.
  { name: 'default', label: 'default' },
  { name: 'manual', label: '人工' },
]

function renderHome(path = '/') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <HomePage />
    </MemoryRouter>,
  )
}

// The filters live inside a collapsed "advanced" panel.
async function openFilters() {
  await userEvent.click(await screen.findByText('home.advanced'))
}

beforeEach(() => {
  state.resp = { ...base }
  state.urls = []
})

describe('the version filter', () => {
  it('is absent while there is only one written form', async () => {
    renderHome()
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))
    await openFilters()
    // The other filters are there; this one is not, because every setting of it would mean the same.
    // getAllByText: each filter's key renders twice, as the label and as the placeholder.
    expect(screen.getAllByText('home.category').length).toBeGreaterThan(0)
    expect(screen.queryAllByText('home.version')).toHaveLength(0)
  })

  it('offers the written forms the server says are visible, by their labels', async () => {
    state.resp = { ...base, versions: twoVersions }
    renderHome()
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))
    await openFilters()
    expect((await screen.findAllByText('home.version')).length).toBeGreaterThan(0)

    // The options are the server's labels, not the internal names: "人工" is what an author reads.
    await userEvent.click(document.querySelector('#version') as HTMLElement)
    expect(await screen.findByTitle('人工')).toBeTruthy()
    expect(screen.getByTitle('versions.default')).toBeTruthy()
  })

  it('asks the server for the chosen version', async () => {
    state.resp = { ...base, versions: twoVersions }
    renderHome()
    await waitFor(() => expect(state.urls.length).toBeGreaterThan(0))
    await openFilters()

    const select = document.querySelector('#version') as HTMLElement
    expect(select).toBeTruthy()
    await userEvent.click(select)
    await userEvent.click(await screen.findByTitle('人工'))
    await userEvent.click(screen.getByText('home.search'))

    await waitFor(() => expect(state.urls.some((u) => u.includes('version=manual'))).toBe(true))
  })

  it('comes back selected when the URL already carries it', async () => {
    state.resp = { ...base, versions: twoVersions }
    renderHome('/?version=manual')
    // The very first request already carries it: the URL is the source of truth, not the form.
    await waitFor(() => expect(state.urls[0]).toContain('version=manual'))
    await openFilters()
    // And the control shows it, so the reader can see which filter is on and clear it.
    expect(await screen.findByTitle('人工')).toBeTruthy()
  })
})
