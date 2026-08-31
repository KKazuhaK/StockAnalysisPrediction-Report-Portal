import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { App } from 'antd'
import OrgUnitDetail from './OrgUnitDetail'
import type { UserGroupRow } from '../../api/types'

const put = vi.hoisted(() => vi.fn())
const get = vi.hoisted(() => vi.fn())
const del = vi.hoisted(() => vi.fn())
vi.mock('../../api/client', () => ({
  api: { get, put, post: vi.fn(), del },
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
  }),
}))

const g = (o: Partial<UserGroupRow>): UserGroupRow =>
  ({ id: 2, name: 'Clients', members: 0, weight: null, ...o }) as UserGroupRow

const DEF = g({ id: 1, name: 'Default', is_default: true, weight: 2, allow_urgent: true, max_queued: 5, run_window: '' })

const mount = (target: UserGroupRow, groups: UserGroupRow[] = [DEF, target]) =>
  render(
    <App>
      <OrgUnitDetail group={target} groups={groups} onChanged={vi.fn()} onDeleted={vi.fn()} />
    </App>,
  )

describe('OrgUnitDetail', () => {
  beforeEach(() => {
    put.mockReset().mockResolvedValue({})
    del.mockReset().mockResolvedValue({})
    get.mockReset().mockResolvedValue({ targets: [], granted: [] })
  })

  // The whole point of the rewrite: an inheriting setting must show WHAT it inherits and FROM
  // WHERE. The old form showed a switch labelled "inherit from the default group" above a greyed
  // field with no value in it — and for the run window, a greyed field showing "9-18", which is the
  // example from the hint text and reads exactly like a real value.
  it('names the inherited value and its source, and never shows a bare example as a value', () => {
    mount(g({ id: 2 }))
    const labels = screen.getAllByText(/ou\.inheritedAs/).map((n) => n.textContent ?? '')
    // Max queued inherits 5 from Default; the run window inherits "no restriction", not "9-18".
    expect(labels.some((l) => l.includes('"value":"5"') && l.includes('"from":"Default"'))).toBe(true)
    expect(labels.some((l) => l.includes('ou.anyHour'))).toBe(true)
    expect(labels.some((l) => l.includes('"value":"9-18"'))).toBe(false)
  })

  it('priority names the SYSTEM default, which is a different source from the Default group', () => {
    mount(g({ id: 2 }))
    // And it says it ONCE: "inherit the system default — the system default" is what naming both a
    // source and a value gets you when the source has no value worth printing.
    const label = screen.getByText(/ou\.inheritedFrom/).textContent ?? ''
    expect(label).toContain('ou.systemDefault')
    expect(screen.queryByText(/ou\.inheritedAs.*ou\.systemDefault/)).toBeNull()
  })

  // Choosing "set here" is what reveals the input. A disabled control next to a selected "inherit"
  // is the ambiguity the old form had.
  it('reveals the override input only once the override is chosen', () => {
    mount(g({ id: 2, max_queued: 9 }))
    expect(screen.getByDisplayValue('9')).toBeTruthy()
    // Switch that one back to inheriting and the input goes away.
    const inheritRadios = screen.getAllByText(/ou\.inheritedAs.*"value":"5"/)
    fireEvent.click(inheritRadios[0])
    expect(screen.queryByDisplayValue('9')).toBeNull()
  })

  it('saves nulls for the settings left inheriting, and values for the ones set here', async () => {
    mount(g({ id: 2, max_queued: 9 }))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.max_queued).toBe(9) // set here
    expect(body.run_window).toBeNull() // inheriting
    expect(body.weight).toBeNull() // inheriting
  })

  // The urgent policy is one control mapped onto three stored fields, so "allowed" and "unlimited"
  // can never contradict each other the way the two tags did.
  it('collapses the urgent policy into three consistent fields', async () => {
    mount(g({ id: 2, allow_urgent: true, urgent_unlimited: true, weight: 3 }))
    fireEvent.click(screen.getByText('common.save'))
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.allow_urgent).toBe(true)
    expect(body.urgent_unlimited).toBe(true)
    expect(body.weight).toBe(0) // a ticket count under "unlimited" would be the contradiction
  })

  it('the Default OU has no parent, no restriction and no delete', () => {
    mount(DEF, [DEF])
    expect(screen.queryByText('users.parentOu')).toBeNull()
    expect(screen.queryByText('ou.sectionTenancy')).toBeNull()
    expect(screen.queryByText('ou.deleteOu')).toBeNull()
  })

  // The quota is a number AND a window, and both have to survive the round trip together — the
  // server refuses to inherit one without the other, so a panel that sent only the number would
  // silently reset a monthly cap to daily.
  it('saves the quota period alongside the number', async () => {
    mount(g({ id: 2, restricted: true, restricted_effective: true, daily_run_quota: 20, run_quota_period: 'month' }))
    fireEvent.click(screen.getAllByText('common.save')[0]) // a restricted OU also renders the allow-list's save
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[0][1] as Record<string, unknown>
    expect(body.daily_run_quota).toBe(20)
    expect(body.run_quota_period).toBe('month')
  })

  // The quota inherits down the OU TREE, so the hint has to name the ancestor's cap. It used to
  // fall back to 0 and offer "inherit — unlimited" under a parent that caps its subtree at 20.
  it('offers the ancestor’s cap, with its window, as what inheriting would mean', () => {
    const clients = g({ id: 2, name: 'Clients', parent_id: 1, daily_run_quota: 20, run_quota_period: 'month' })
    const acme = g({ id: 3, name: 'Acme', parent_id: 2, restricted: true, restricted_effective: true })
    mount(acme, [DEF, clients, acme])
    const labels = screen.getAllByText(/ou\.inheritedAs/).map((n) => n.textContent ?? '')
    expect(labels.some((l) => l.includes('20 ou.period.month'))).toBe(true)
    expect(labels.some((l) => l.includes('ou.unlimited') && l.includes('users.parentOu'))).toBe(false)
  })

  // The column header is the only way to revoke a surface across a dozen workflows, and it was
  // computed over ALL rows while acting on only the enabled ones. One workflow switched off left
  // the header permanently unchecked, so clicking it could grant the surface and never take it back.
  it('the column toggle reflects — and can clear — the rows it actually governs', async () => {
    get.mockResolvedValue({
      targets: [
        { id: 1, name: 'A', surfaces: ['run', 'batch'] },
        { id: 2, name: 'B', surfaces: ['run', 'batch'] }, // left OFF: the toggle must ignore it
      ],
      granted: [{ target_id: 1, surfaces: ['run'] }],
    })
    mount(g({ id: 3, restricted: true, restricted_effective: true }))
    await screen.findByText('A')

    // Every row the toggle governs (only workflow A) already has 'run', so it reads as checked.
    const runHeader = screen.getByText('users.surface.run').closest('th')!
    const box = runHeader.querySelector('input[type="checkbox"]') as HTMLInputElement
    expect(box.checked).toBe(true)

    // And unticking it revokes, rather than being a no-op that can only ever grant.
    fireEvent.click(box)
    await waitFor(() => expect((runHeader.querySelector('input[type="checkbox"]') as HTMLInputElement).checked).toBe(false))
    fireEvent.click(screen.getAllByText('common.save')[1])
    await waitFor(() => expect(put).toHaveBeenCalled())
    const body = put.mock.calls[put.mock.calls.length - 1][1] as { granted: { target_id: number; surfaces: string[] }[] }
    expect(body.granted.find((x) => x.target_id === 1)?.surfaces).toEqual([])
  })

  it('shows the allow-list only for a restricted OU, where it governs anything', () => {
    mount(g({ id: 2 }))
    expect(screen.queryByText('ou.sectionTargets')).toBeNull()
    mount(g({ id: 3, restricted: true, restricted_effective: true }))
    expect(screen.getAllByText('ou.sectionTargets').length).toBeGreaterThan(0)
  })
})

// Deleting an OU sweeps the announcement grants naming it, which can leave an announcement
// addressed to nobody: still enabled, still "live", reaching no one and reporting nothing. The
// operator is shown that while it is still avoidable, and decides. The server refuses without a
// decision too (409), so this dialog is the place to give one rather than the only thing stopping it.
describe('OrgUnitDetail — deleting an OU that announcements are addressed to', () => {
  beforeEach(() => {
    put.mockReset().mockResolvedValue({})
    del.mockReset().mockResolvedValue({})
  })

  const clickDelete = async () => {
    fireEvent.click(screen.getByRole('button', { name: /ou\.deleteOu/ }))
    // antd Popconfirm's own OK, before the impact dialog appears
    const ok = await screen.findAllByRole('button')
    fireEvent.click(ok.find((b) => /OK|确 定|确定/.test(b.textContent || '')) as HTMLElement)
  }

  it('asks what to do, and sends the answer, when an announcement would be left with nobody', async () => {
    get.mockImplementation((url: string) =>
      url.endsWith('/announcements')
        ? Promise.resolve({ affected: [{ id: 5, title: '华东停电', enabled: true, orphaned: true }] })
        : Promise.resolve({ targets: [], granted: [] }),
    )
    mount(g({ id: 2, name: '华东' }))
    await clickDelete()

    expect(await screen.findByText('华东停电')).toBeTruthy()
    expect(screen.getByText('ou.announcementOrphaned')).toBeTruthy()
    expect(del).not.toHaveBeenCalled() // nothing is deleted until the question is answered

    // The dialog's confirm carries the same label as the page's button, so scope to the dialog.
    const dialog = screen.getByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: /ou\.deleteOu/ }))
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/admin/groups/2?orphans=disable'))
  })

  it('goes straight through when every affected announcement keeps other recipients', async () => {
    get.mockImplementation((url: string) =>
      url.endsWith('/announcements')
        ? Promise.resolve({ affected: [{ id: 5, title: '两地停电', enabled: true, orphaned: false }] })
        : Promise.resolve({ targets: [], granted: [] }),
    )
    mount(g({ id: 2, name: '华东' }))
    await clickDelete()

    // No decision to make, so no dialog: the announcement goes on working.
    await waitFor(() => expect(del).toHaveBeenCalledWith('/api/admin/groups/2'))
    expect(screen.queryByText('两地停电')).toBeNull()
  })
})
