import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import { Space, Table } from 'antd'
import { SortableItem, SortableWrapper, sortableTableComponents } from './dnd'

// The failure this file exists for is silent, and it is easy to reintroduce.
//
// SortableWrapper applies restrictToParentElement, which clamps the drag transform to the bounding
// box of the dragged node's PARENT ELEMENT. That is what keeps a row inside its list — but only
// while every sortable node shares one tall parent. Interpose a per-child wrapper and each row's
// parent becomes a box exactly its own size: the transform clamps to zero, the row neither follows
// the pointer nor changes place, and nothing anywhere reports an error.
//
// antd's <Space> is precisely such a wrapper — it puts every child in its own `.ant-space-item` —
// and two pages shipped with the items wrapped in one. So SortableWrapper renders the container
// itself, and these tests pin that it stays the direct parent.

const rows = ['a', 'b', 'c']

function renderList(children: React.ReactNode) {
  return render(
    <SortableWrapper ids={rows} onReorder={() => {}}>
      {children}
    </SortableWrapper>,
  )
}

describe('SortableWrapper', () => {
  it('makes every sortable row a sibling of the others', () => {
    const { container } = renderList(
      rows.map((id) => (
        <SortableItem key={id} id={id}>
          <div>{id}</div>
        </SortableItem>
      )),
    )

    const nodes = Array.from(container.querySelectorAll('[data-sortable-id]'))
    expect(nodes.map((n) => n.getAttribute('data-sortable-id'))).toEqual(rows)
    const parents = new Set(nodes.map((n) => n.parentElement))
    expect(parents.size).toBe(1)
  })

  // The container has to come from the wrapper, not from the caller: a caller who reaches for
  // <Space> to get the gap breaks the clamp, and the gap is exactly why they would.
  it('provides the vertical gap itself, so no caller needs a spacing wrapper', () => {
    const { container } = renderList(
      rows.map((id) => (
        <SortableItem key={id} id={id}>
          <div>{id}</div>
        </SortableItem>
      )),
    )
    const list = container.querySelector('[data-sortable-id]')!.parentElement as HTMLElement
    expect(list.style.display).toBe('flex')
    expect(list.style.flexDirection).toBe('column')
    expect(list.style.gap).toBeTruthy()
  })

  // Not a supported arrangement — a demonstration of what goes wrong, so the reason the assertion
  // above matters is written down rather than remembered.
  it('is defeated by a per-child spacing wrapper, which is why one is never used', () => {
    const { container } = renderList(
      <Space direction="vertical">
        {rows.map((id) => (
          <SortableItem key={id} id={id}>
            <div>{id}</div>
          </SortableItem>
        ))}
      </Space>,
    )
    const nodes = Array.from(container.querySelectorAll('[data-sortable-id]'))
    const parents = new Set(nodes.map((n) => n.parentElement))
    // Three rows, three different parents: each one boxed at its own size, so restrictToParentElement
    // has nothing to move within.
    expect(parents.size).toBe(nodes.length)
    expect(nodes[0].parentElement?.className).toContain('ant-space-item')
  })
})

// The table half of the same "the ids have to agree" problem, and it bit a real page.
//
// antd passes data-row-key through with whatever type the rowKey produced, so `rowKey="id"` over
// numeric ids gives a number — while SortableWrapper's ids are strings. dnd-kit matches a sortable
// to its context by identity, so 1 never finds "1": the row registers at index -1 and refuses to
// sort, with nothing to say why. SortableRow normalizes, so a page cannot get this wrong.
describe('SortableRow', () => {
  const targets = [
    { id: 1, name: 'first' },
    { id: 2, name: 'second' },
  ]

  it('registers a numeric rowKey under the string id the context was given', () => {
    const ids = targets.map((t) => String(t.id))
    const { container } = render(
      <SortableWrapper ids={ids} onReorder={() => {}}>
        <Table
          rowKey="id"
          pagination={false}
          dataSource={targets}
          columns={[{ title: 'n', dataIndex: 'name' }]}
          components={sortableTableComponents}
        />
      </SortableWrapper>,
    )

    const rows = Array.from(container.querySelectorAll('tbody [data-sortable-id]'))
    expect(rows.map((r) => r.getAttribute('data-sortable-id'))).toEqual(ids)
    // And they are siblings, as the non-table list is: a <tbody> holds every row.
    expect(new Set(rows.map((r) => r.parentElement)).size).toBe(1)
  })
})
