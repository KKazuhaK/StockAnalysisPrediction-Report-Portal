import { describe, it, expect } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { Space, Table } from 'antd'
import { DragHandle, SortableItem, SortableWrapper, sortableTableComponents } from './dnd'

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
// Everything below is about who is draggable and who only says so.
//
// DndContext renders dnd-kit's screen-reader instructions unconditionally — "To pick up a draggable
// item, press the space bar" — and useSortable hands out `attributes` marking a node role="button",
// tabIndex=0 and aria-roledescription="sortable". Neither is a claim the library checks: put the
// attributes on the wrong node and it announces the wrong element, register no KeyboardSensor and
// the key it tells you to press does nothing. Both were true here, on every sortable list in the
// console at once, and neither shows up in a test that only drags with a pointer.

describe('the drag handle is the draggable thing', () => {
  it('marks the handle sortable, not the row around it', () => {
    const { container } = render(
      <SortableWrapper ids={['a']} onReorder={() => {}}>
        <SortableItem id="a">
          <DragHandle label="Reorder" />
          <button>Edit</button>
        </SortableItem>
      </SortableWrapper>,
    )
    const handle = screen.getByLabelText('Reorder')
    expect(handle.getAttribute('aria-roledescription')).toBe('sortable')
    expect(handle.getAttribute('aria-describedby')).toBeTruthy()

    // The row must NOT be a button. It wraps the handle and an Edit button, so role="button" here is
    // invalid ARIA around two real buttons, and it puts a dead tab stop in front of every row —
    // dead because dnd-kit only listens on the activator, which is the handle.
    const row = container.querySelector('[data-sortable-id]')!
    for (const a of ['role', 'tabindex', 'aria-roledescription', 'aria-describedby']) {
      expect(row.hasAttribute(a)).toBe(false)
    }
  })

  it('leaves a table row its implicit row role', () => {
    const { container } = render(
      <SortableWrapper ids={['1']} onReorder={() => {}}>
        <Table
          rowKey="id"
          pagination={false}
          dataSource={[{ id: 1, name: 'x' }]}
          columns={[{ title: 'n', dataIndex: 'name' }]}
          components={sortableTableComponents}
        />
      </SortableWrapper>,
    )
    // role="button" on a <tr> overrides the implicit `row`, and a screen reader can then no longer
    // walk the table by row and column at all.
    const row = container.querySelector('tbody [data-sortable-id]')!
    expect(row.hasAttribute('role')).toBe(false)
    expect(row.hasAttribute('tabindex')).toBe(false)
  })

  it('does not make the empty-table placeholder draggable', () => {
    const { container } = render(
      <SortableWrapper ids={[]} onReorder={() => {}}>
        <Table
          rowKey="id"
          pagination={false}
          dataSource={[]}
          columns={[{ title: 'n', dataIndex: 'name' }]}
          components={sortableTableComponents}
        />
      </SortableWrapper>,
    )
    // rc-table renders "no data" through the same row component, with no row key. It used to
    // register as a sortable called "undefined" and announce itself as a draggable item.
    expect(container.querySelector('[data-sortable-id]')).toBeNull()
    const placeholder = container.querySelector('tbody tr')!
    expect(placeholder.className).toContain('ant-table-placeholder')
    expect(placeholder.hasAttribute('role')).toBe(false)
    expect(placeholder.hasAttribute('tabindex')).toBe(false)
  })
})

describe('keyboard dragging', () => {
  it('picks an item up on the key the instructions promise', () => {
    render(
      <SortableWrapper ids={['a', 'b']} onReorder={() => {}}>
        {['a', 'b'].map((id) => (
          <SortableItem key={id} id={id}>
            <DragHandle label={`Reorder ${id}`} />
          </SortableItem>
        ))}
      </SortableWrapper>,
    )
    // dnd-kit renders this whether or not a KeyboardSensor is registered, so it is a promise made on
    // the code's behalf. With only a PointerSensor it was a lie, on lists where dragging is the only
    // way to reorder at all — there is no move-up button and no order field anywhere in the console.
    expect(document.body.textContent).toContain('press the space bar')

    const handle = screen.getByLabelText('Reorder a')
    expect(handle.getAttribute('aria-pressed')).toBeNull()
    fireEvent.keyDown(handle, { key: ' ', code: 'Space' })
    expect(handle.getAttribute('aria-pressed')).toBe('true')
    // ...and the row is actually held: dnd-kit only applies the dragging style to an active item.
    const row = document.querySelector('[data-sortable-id="a"]') as HTMLElement
    expect(row.style.zIndex).toBe('999')
  })
})
