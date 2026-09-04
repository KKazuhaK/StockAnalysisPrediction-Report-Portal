import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App, Grid } from 'antd'
import AuditPage from './AuditPage'

// The log is only useful if a row can be read without cross-referencing anything: who, what, which
// object, when. Two of those need help — a machine caller has no username, and an OU id means
// nothing on screen.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), del: vi.fn() }))
vi.mock('../../api/client', () => ({
  api: apiMock,
  ApiError: class extends Error {},
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string, fb?: unknown) => (typeof fb === 'string' ? fb : k) }),
}))

const RESP = {
  total: 2,
  actions: ['report.read', 'grant.change'],
  ou_names: { '7': '客户A' },
  items: [
    { id: 2, at: '2026-08-01 09:00:00', actor: '', actor_ou: 0, action: 'report.read',
      target_type: 'report', target_id: '42', detail: '{"symbol":"600519"}' },
    { id: 1, at: '2026-08-01 08:00:00', actor: 'client@corp.example', actor_ou: 7,
      action: 'grant.change', target_type: 'version', target_id: '对外版',
      detail: '{"before":[],"after":["u:client@corp.example"]}' },
  ],
}

// Mirrors user-event's own precondition: an element is reachable only if neither it nor any
// ancestor switches pointer events off.
const reachable = (el: Element | null): boolean => {
  // A node that is no longer in the document is not reachable, and saying so is the whole point.
  // getComputedStyle on a detached node reports no pointer-events at all, so the loop below would
  // walk it and conclude "reachable" — which is exactly backwards, and exactly what happens to a
  // node captured before antd re-rendered the table.
  if (!el || !el.isConnected) return false
  for (let n: Element | null = el; n; n = n.parentElement) {
    if (getComputedStyle(n).pointerEvents === 'none') return false
  }
  return true
}

const mount = () =>
  render(
    <App>
      <AuditPage />
    </App>,
  )

// jsdom's matchMedia never matches, so antd would report every breakpoint as absent and the page
// would render its phone layout under test. Say which one is being tested instead of inheriting it.
const screenWidth = (wide: boolean) =>
  vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: wide, lg: wide } as ReturnType<typeof Grid.useBreakpoint>)

describe('AuditPage', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.get.mockResolvedValue(RESP)
    screenWidth(true)
  })

  it('names the OU the actor was in, rather than showing a bare id', async () => {
    mount()
    expect(await screen.findByText('客户A')).toBeTruthy()
  })

  it('says a machine acted instead of leaving the actor blank', async () => {
    mount()
    // An empty cell reads as a bug; "(API token)" reads as a fact.
    expect(await screen.findByText('audit.machine')).toBeTruthy()
  })

  it('shows the object and the detail, so a line is readable on its own', async () => {
    mount()
    expect(await screen.findByText(/对外版/)).toBeTruthy()
    expect(screen.getByText(/600519/)).toBeTruthy()
    // A grant change carries both sides — the current state cannot answer "when did they gain it".
    // Rendered rather than dumped as JSON, but with the empty side still saying it was empty.
    expect(screen.getByText(/before=— · after=u:client@corp\.example/)).toBeTruthy()
  })

  // The detail column is a sentence now, which is what somebody scanning the log wants. Somebody
  // investigating wants the opposite: every field exactly as stored, including the ones the
  // sentence leaves out for being uninformative. One click, not a second page.
  it('opens a row in full, with the stored payload verbatim', async () => {
    mount()
    await screen.findAllByTitle('audit.details')
    // A row appearing is not the same as a row being clickable: while the table is loading antd
    // blurs it with pointer-events: none, and user-event refuses to click through that. Waiting
    // for the button to be genuinely reachable is what a person does, and it is what this test
    // failed to do on a loaded CI runner while passing on an idle laptop.
    //
    // Re-queried inside the wait, and again for the click, rather than held from before it. Holding
    // one was the remaining half of the same flake: the wait is satisfied the moment the table
    // re-renders and replaces the node, because a DETACHED node reports no pointer-events and so
    // looks reachable — and the click then lands on a node that is no longer in the page. Under load
    // the re-render is likelier, which is why this failed where it did.
    await waitFor(() => expect(reachable(screen.getAllByTitle('audit.details')[1])).toBe(true))
    await userEvent.click(screen.getAllByTitle('audit.details')[1]) // the grant change: a payload worth reading

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('{"before":[],"after":["u:client@corp.example"]}')).toBeTruthy()
    // Everything the row carries, not just its detail — an investigation needs the actor, the
    // address and the object together, in one place.
    // Exact, because the payload below also mentions the account: this asserts the actor FIELD,
    // resolved OU and all, not merely that the name appears somewhere in the dialog.
    expect(within(dialog).getByText('client@corp.example · 客户A')).toBeTruthy()
    expect(within(dialog).getByText('version 对外版')).toBeTruthy()
  })

  // A phone cannot hold the six columns, and antd's answer is to squeeze them until a Chinese
  // sentence wraps one character per line. Below md the rows are cards instead: no table at all,
  // and the card itself is what opens the full record — there is no room for a button column.
  describe('on a phone', () => {
    beforeEach(() => screenWidth(false))

    it('renders rows as cards rather than as a squeezed table', async () => {
      const { container } = mount()
      await screen.findByText(/对外版/)
      expect(container.querySelector('table')).toBeNull()
      expect(container.querySelectorAll('.rp-audit-row').length).toBe(2)
      // Same facts as the table row, so nothing is lost by dropping the columns.
      expect(screen.getByText('客户A')).toBeTruthy()
      expect(screen.getByText(/before=— · after=u:client@corp\.example/)).toBeTruthy()
    })

    it('opens the full record when a card is tapped', async () => {
      const { container } = mount()
      await screen.findByText(/对外版/)
      const cards = container.querySelectorAll('.rp-audit-row')
      await userEvent.click(cards[1] as Element)
      const dialog = await screen.findByRole('dialog')
      expect(within(dialog).getByText('{"before":[],"after":["u:client@corp.example"]}')).toBeTruthy()
    })

    // Folded away, the filters would silently explain an empty page, so the button carries a dot
    // whenever one of them is set. Nothing is set on arrival.
    it('keeps the filters folded but reachable', async () => {
      mount()
      await screen.findByText(/对外版/)
      expect(screen.queryByPlaceholderText('audit.ipFilter')).toBeNull()
      await userEvent.click(screen.getByRole('button', { name: /audit\.filters/ }))
      expect(screen.getByPlaceholderText('audit.ipFilter')).toBeTruthy()
    })
  })

  it('asks the server for a page, not the whole table', async () => {
    mount()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalled())
    expect(String(apiMock.get.mock.calls[0][0])).toContain('limit=50')
  })
})
