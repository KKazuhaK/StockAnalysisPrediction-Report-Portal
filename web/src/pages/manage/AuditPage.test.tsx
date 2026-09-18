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
// The values this fake bundle has been taught. Everything else falls back — which is the behaviour
// the enum rendering turns on, and which a stub that answered every lookup could not exercise.
const vocab = vi.hoisted(
  () => new Set(['audit.v.reason.bad_password', 'audit.v.field.primary_group', 'audit.t.batch_job']),
)
vi.mock('react-i18next', () => {
  // One `t`, handed back from one object, because that is what the real hook does.
  //
  // A fresh `t` per call made every render produce a new `load` callback on the page, so its effect
  // re-ran and re-fetched on EVERY render: the table never stopped spinning (antd sets
  // pointer-events: none on a spinning table), and clicking a row became a race. A laptop won it and
  // CI lost it, which is how this shipped red.
  //
  // A STRING second argument is i18next's default-value form, not interpolation: it is what comes
  // back when the bundle has no such key. Honouring the distinction is what makes the stub usable
  // for a renderer whose whole point is telling "known vocabulary" from "not taught yet".
  const t = (k: string, fb?: unknown) => (typeof fb === 'string' ? (vocab.has(k) ? k : fb) : k)
  const api = { t }
  return { useTranslation: () => api }
})

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

// The detail renders as parts, not as one string: an identifier and its value are separate elements
// so the value can carry the weight and the field name can be dimmed (see lib/auditDetail.ts). The
// reader sees one line, so the test reads the row back as one line.
const detailText = (container: HTMLElement) =>
  [...container.querySelectorAll('.rp-audit-detail')].map((d) => d.textContent).join('\n')

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
    const { container } = mount()
    expect(await screen.findByText(/对外版/)).toBeTruthy()
    expect(screen.getByText('600519')).toBeTruthy()
    // A grant change carries both sides — the current state cannot answer "when did they gain it".
    // Rendered as a change rather than as two fields, so the empty side is what a "+" means.
    expect(detailText(container)).toContain('audit.diff.added audit.t.user client@corp.example')
  })

  // The pair arrives alphabetically ("after" first) because the server stores it in a map. Rendered
  // as a change, the order it was written in stops mattering and the membership is explicit.
  it('marks what a change added and what it removed', async () => {
    apiMock.get.mockResolvedValue({
      ...RESP,
      items: [
        { id: 1, at: '2026-08-01 08:00:00', actor: 'admin', actor_ou: 0, action: 'grant.change',
          target_type: 'group', target_id: '12',
          detail: '{"after":["u:b@corp.example","u:c@corp.example"],"before":["u:a@corp.example"]}' },
      ],
    })
    const { container } = mount()
    await screen.findByText('group 12')
    // One line: every member named with its kind, under a word that says which way it moved. The
    // t() stub returns the key, so what shows is the wording the renderer chose.
    const text = detailText(container)
    expect(text).toBe(
      'audit.diff.added audit.t.user b@corp.example、audit.t.user c@corp.example' +
        ' · audit.diff.removed audit.t.user a@corp.example',
    )
    // The fields the pair replaced must not also print.
    expect(text).not.toContain('before')
    expect(text).not.toContain('after')
  })

  // A value from a closed vocabulary the server defines is a word, not a field: it is worth
  // translating, and it is part of the sentence rather than a parameter hanging off it.
  it('renders a value from a closed vocabulary as a word, not as a field', async () => {
    apiMock.get.mockResolvedValue({
      ...RESP,
      items: [
        { id: 1, at: '2026-08-01 08:00:00', actor: '', actor_ou: 0, action: 'auth.login_failed',
          target_type: 'user', target_id: 'alice', detail: '{"reason":"bad_password"}' },
      ],
    })
    const { container } = mount()
    await screen.findByText(/alice/)
    // The t() stub returns the key, so what shows is the vocabulary key the renderer chose — which
    // is the part under test. The field it replaced is gone.
    const column = detailText(container)
    expect(column).toContain('audit.v.reason.bad_password')
    expect(column).not.toContain('reason=')
  })

  // A change whose sides are not lists has no membership to report, so it is the two values under
  // the field that named them. The label is part of the same line — which is why testing the group
  // list for emptiness swallowed both values and left a bare field label.
  it('shows both values of a change that is not a list', async () => {
    apiMock.get.mockResolvedValue({
      ...RESP,
      ou_names: { '2': 'Default', '9': 'ext-demo' },
      items: [
        { id: 1, at: '2026-08-01 08:00:00', actor: 'admin', actor_ou: 0, action: 'user.change',
          target_type: 'user', target_id: 'extuser',
          detail: '{"field":"primary_group","from":2,"to":9}' },
      ],
    })
    const { container } = mount()
    await screen.findByText('user extuser') // the object column; its kind word is not in the fake bundle
    // The ids are OU ids, so they are resolved the same way the actor column resolves one.
    expect(detailText(container)).toBe('audit.v.field.primary_group Default → ext-demo')
  })

  // A row that says what happened shows just that. Everything the submission carried — its inputs,
  // its priority, whether it asked for urgent — is behind the full-record button, because a page of
  // rows holding every parameter of every run reads as one wall of key=value.
  it('keeps a run’s parameters out of the column and in the record', async () => {
    apiMock.get.mockResolvedValue({
      ...RESP,
      items: [
        { id: 1, at: '2026-08-01 08:00:00', actor: 'admin', actor_ou: 0, action: 'run.submit',
          target_type: 'batch_job', target_id: '782',
          detail: '{"target":"Deep Research","priority":"30","retries":2,"inputs":"query=x"}' },
      ],
    })
    const { container } = mount()
    await screen.findByText('audit.t.batch_job 782')
    expect(detailText(container)).toContain('audit.d.run')
    expect(detailText(container)).not.toContain('priority')
    expect(detailText(container)).not.toContain('query=x')

    await userEvent.click(screen.getAllByTitle('audit.details')[0])
    const record = detailText(await screen.findByRole('dialog'))
    expect(record).toContain('priority 30')
    expect(record).toContain('retries 2')
    expect(record).toContain('inputs query=x')
  })

  // The target column used to read "version <name>" only because target_type happened to be spelled
  // the same in English. It is our own closed vocabulary, so it gets a name.
  it('names the kind of object a row acted on rather than printing its raw type', async () => {
    apiMock.get.mockResolvedValue({
      ...RESP,
      items: [
        { id: 1, at: '2026-08-01 08:00:00', actor: 'admin', actor_ou: 0, action: 'run.submit',
          target_type: 'batch_job', target_id: '782', detail: '{}' },
      ],
    })
    mount()
    expect(await screen.findByText('audit.t.batch_job 782')).toBeTruthy()
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
      expect(detailText(container)).toContain('audit.diff.added audit.t.user client@corp.example')
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
    // Exactly once, and this is load-bearing rather than tidiness. A mock that handed back a fresh
    // `t` on every call made the page's effect re-run and re-fetch on every render: ten calls in
    // 400ms and climbing, the table spinning the whole time (antd sets pointer-events: none on a
    // spinning table), and every click on a row a race. A laptop won that race; CI lost it.
    await new Promise((r) => setTimeout(r, 400))
    expect(apiMock.get.mock.calls.length).toBe(1)
  })
})
