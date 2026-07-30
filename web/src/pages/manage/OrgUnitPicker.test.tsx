import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { App } from 'antd'
import OrgUnitPicker, { subtreeOf } from './OrgUnitPicker'
import type { UserGroupRow } from '../../api/types'

vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (k: string) => k }) }))

// OUs are a tree: restriction is sticky down a subtree and quota/weight inherit root-to-leaf. The
// picker exists so that shape is visible, and the selection has to mean the same thing the tree
// means — pick a parent, get everyone its settings govern.

const g = (id: number, name: string, parent = 0, extra: Partial<UserGroupRow> = {}): UserGroupRow =>
  ({ id, name, parent_id: parent, members: 0, weight: null, ...extra }) as UserGroupRow

//    1 Root
//    ├── 2 Clients ── 4 Clients APAC
//    └── 3 Staff
//    5 Detached (a second root)
const TREE = [g(1, 'Root', 0, { is_default: true }), g(2, 'Clients', 1), g(3, 'Staff', 1), g(4, 'Clients APAC', 2), g(5, 'Detached')]

describe('subtreeOf', () => {
  it('expands a parent to everything beneath it', () => {
    expect([...subtreeOf(TREE, [2])].sort()).toEqual([2, 4])
    expect([...subtreeOf(TREE, [1])].sort()).toEqual([1, 2, 3, 4])
  })

  it('a leaf is only itself, and an empty selection matches nothing', () => {
    expect([...subtreeOf(TREE, [4])]).toEqual([4])
    expect(subtreeOf(TREE, []).size).toBe(0)
  })

  it('merges overlapping selections without duplicating', () => {
    expect([...subtreeOf(TREE, [1, 2, 4])].sort()).toEqual([1, 2, 3, 4])
  })

  // The server refuses to build one, but this runs on whatever the server sent, and a hung admin
  // page is a worse failure than a wrong answer.
  it('terminates on a cycle', () => {
    const cyclic = [g(1, 'A', 2), g(2, 'B', 1)]
    expect([...subtreeOf(cyclic, [1])].sort()).toEqual([1, 2])
  })
})

describe('OrgUnitPicker', () => {
  const mount = (props: Partial<React.ComponentProps<typeof OrgUnitPicker>> = {}) => {
    const onSelect = vi.fn()
    const onScopedChange = vi.fn()
    const onManage = vi.fn()
    const r = render(
      <App>
        <OrgUnitPicker
          groups={TREE}
          scoped={false}
          onScopedChange={onScopedChange}
          selected={[]}
          onSelect={onSelect}
          onManage={onManage}
          {...props}
        />
      </App>,
    )
    return { ...r, onSelect, onScopedChange, onManage }
  }

  it('renders the hierarchy, not a flat list', () => {
    mount()
    for (const name of ['Root', 'Clients', 'Staff', 'Clients APAC', 'Detached']) {
      expect(screen.getByText(name)).toBeTruthy()
    }
    // A child is nested under its parent rather than a sibling of it.
    const parent = screen.getByText('Clients').closest('.ant-tree-treenode')
    const child = screen.getByText('Clients APAC').closest('.ant-tree-treenode')
    expect(parent).toBeTruthy()
    expect(child).toBeTruthy()
    expect(parent).not.toBe(child)
  })

  it('selecting an OU also turns the scope on, so it is one action not two', () => {
    const { onSelect, onScopedChange } = mount()
    fireEvent.click(screen.getByText('Clients'))
    expect(onSelect).toHaveBeenCalledWith([2])
    expect(onScopedChange).toHaveBeenCalledWith(true)
  })

  it('search keeps the ancestors of a match, or the match would be unreachable', () => {
    mount()
    fireEvent.change(screen.getByPlaceholderText('ou.search'), { target: { value: 'APAC' } })
    expect(screen.getByText('Clients APAC')).toBeTruthy()
    expect(screen.getByText('Clients')).toBeTruthy() // the ancestor survives
    expect(screen.queryByText('Staff')).toBeNull()
    expect(screen.queryByText('Detached')).toBeNull()
  })

  it('narrowing to single select drops the extra selection instead of keeping a hidden filter', () => {
    const { onSelect } = mount({ selected: [2, 3] })
    fireEvent.click(screen.getByText('ou.multi'))
    onSelect.mockClear()
    fireEvent.click(screen.getByText('ou.single'))
    expect(onSelect).toHaveBeenCalledWith([2])
  })

  it('collapses to a single control and comes back', () => {
    mount()
    fireEvent.click(screen.getByRole('button', { name: 'ou.collapse' }))
    // The tree is gone and only the reopen affordance is left.
    expect(screen.queryByText('Detached')).toBeNull()
    expect(screen.queryByText('ou.title')).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'ou.expand' }))
    expect(screen.getByText('Detached')).toBeTruthy()
  })

  it('offers a way through to managing the units themselves', () => {
    const { onManage } = mount()
    fireEvent.click(screen.getByText('ou.manage'))
    expect(onManage).toHaveBeenCalled()
  })
})
