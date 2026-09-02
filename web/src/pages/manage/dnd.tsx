import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { Button } from 'antd'
import { HolderOutlined } from '@ant-design/icons'
import {
  DndContext,
  KeyboardSensor,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import { restrictToParentElement, restrictToVerticalAxis } from '@dnd-kit/modifiers'
import {
  SortableContext,
  arrayMove,
  sortableKeyboardCoordinates,
  useSortable,
  verticalListSortingStrategy,
} from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'

// Drag-to-sort: wire antd Table body rows to @dnd-kit. Use a "drag handle" instead of dragging the
// whole row, to avoid conflicts with the input/dropdown inside a cell. See LinksPage / TypesPage for usage.

interface RowCtx {
  setActivatorNodeRef?: (el: HTMLElement | null) => void
  listeners?: Record<string, any>
  attributes?: Record<string, any>
}
const RowContext = createContext<RowCtx>({})

// The handle carries `attributes`, not the row.
//
// dnd-kit's `attributes` are role="button", tabIndex=0, aria-roledescription="sortable" and an
// aria-describedby pointing at its "press space to pick up" instructions. On the row they describe
// the wrong element: a <tr> loses its implicit `row` role, so a screen reader can no longer walk the
// table by row and column, and a list item that contains a switch, an Edit button and a delete
// confirm becomes one role="button" wrapping all three — which is invalid, and which also puts a
// dead tab stop in front of every row, since Space on the row does nothing (see the sensors below;
// the activator is this button, and dnd-kit ignores a key that lands anywhere else). Here they are
// true: this button is the thing you pick up, and the instructions describe what it does.
export function DragHandle({ label }: { label?: string }) {
  const { setActivatorNodeRef, listeners, attributes } = useContext(RowContext)
  return (
    <Button
      type="text"
      size="small"
      icon={<HolderOutlined />}
      ref={setActivatorNodeRef}
      aria-label={label ?? 'Reorder'}
      style={{ cursor: 'grab', touchAction: 'none' }}
      {...attributes}
      {...listeners}
    />
  )
}

function SortableTableRow(props: React.HTMLAttributes<HTMLTableRowElement> & { 'data-row-key': string | number }) {
  // String(), because antd hands this through with the type the rowKey produced: `rowKey="id"` over
  // numeric ids gives a NUMBER here, while SortableWrapper's ids are strings. dnd-kit matches a
  // sortable to its context by identity, so 1 never finds "1" — the row registers at index -1 and
  // silently refuses to sort, which is what the run-targets table did. Normalizing at the one place
  // that reads the attribute makes every table agree with the wrapper by construction.
  const id = String(props['data-row-key'])
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({
    id,
  })
  const style: React.CSSProperties = {
    ...props.style,
    transform: CSS.Translate.toString(transform),
    transition,
    ...(isDragging ? { position: 'relative', zIndex: 999 } : {}),
  }
  const ctx = useMemo<RowCtx>(
    () => ({ setActivatorNodeRef, listeners, attributes }),
    [setActivatorNodeRef, listeners, attributes],
  )
  return (
    <RowContext.Provider value={ctx}>
      <tr {...props} ref={setNodeRef} data-sortable-id={id} style={style} />
    </RowContext.Provider>
  )
}

export function SortableRow(props: React.HTMLAttributes<HTMLTableRowElement> & { 'data-row-key'?: string | number }) {
  // rc-table renders its "no data" placeholder through this same component, with no row key — so an
  // empty table used to register a sortable with the id "undefined", announced to a screen reader as
  // a draggable item and reachable by Tab. It is one <tr> that says "no data"; there is nothing to
  // sort. Splitting the hook into a child component is what lets this be a plain early return.
  const key = props['data-row-key']
  if (key === undefined) return <tr {...props} />
  return <SortableTableRow {...props} data-row-key={key} />
}

export const sortableTableComponents = { body: { row: SortableRow } }

// SortableItem is the non-table equivalent of SortableRow: a sortable <div> that exposes a
// DragHandle via context. Use it inside SortableWrapper for custom lists (e.g. a mix of
// group headers and links) that a Table can't model.
export function SortableItem({ id, children }: { id: string; children: ReactNode }) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id })
  const style: React.CSSProperties = {
    transform: CSS.Translate.toString(transform),
    transition,
    ...(isDragging ? { position: 'relative', zIndex: 999 } : {}),
  }
  const ctx = useMemo<RowCtx>(
    () => ({ setActivatorNodeRef, listeners, attributes }),
    [setActivatorNodeRef, listeners, attributes],
  )
  return (
    <RowContext.Provider value={ctx}>
      {/* data-sortable-id is for tests and for debugging a list that will not drag: it makes the
          sortable nodes findable, so a test can assert they are siblings (see SortableWrapper).
          `attributes` go on the DragHandle, not here — see the comment there. */}
      <div ref={setNodeRef} data-sortable-id={id} style={style}>
        {children}
      </div>
    </RowContext.Provider>
  )
}

// Pointer and keyboard both, and the keyboard half is not optional: DndContext renders dnd-kit's
// screen-reader instructions unconditionally ("To pick up a draggable item, press the space bar"),
// so a list with only a PointerSensor tells every screen-reader user to press a key that does
// nothing. Since ordering is the whole mechanism on these pages — there is no move-up button and no
// order field anywhere — that left reordering impossible without a mouse while announcing itself as
// available. Registering the sensor is what makes the instructions true.
export function useSortableSensors() {
  return useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 4 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates }),
  )
}

// SortableWrapper provides DndContext + SortableContext + the list container; onReorder receives the
// reordered key sequence.
//
// The container is HERE, not in the caller, and that is the whole reason this function renders a
// div at all. restrictToParentElement clamps the drag transform to the bounding box of the dragged
// node's PARENT ELEMENT, so every sortable node has to share one tall parent for the modifier to
// mean "stay inside the list". Wrap the items in an antd <Space> instead and each one lands in its
// own `.ant-space-item` box, exactly its own size — the transform is then clamped to zero and the
// row neither follows the pointer nor changes place, with no error anywhere to say why. Two pages
// shipped that way before this was understood. Owning the container is what stops it recurring:
// pass SortableItems straight in and set `gap`.
export function SortableWrapper({
  ids,
  onReorder,
  gap = 8,
  children,
}: {
  ids: string[]
  onReorder: (orderedIds: string[]) => void
  /** Vertical space between rows, in px. Ignored when the child is a Table. */
  gap?: number
  children: ReactNode
}) {
  const sensors = useSortableSensors()
  const onDragEnd = ({ active, over }: DragEndEvent) => {
    if (over && active.id !== over.id) {
      const from = ids.indexOf(String(active.id))
      const to = ids.indexOf(String(over.id))
      if (from !== -1 && to !== -1) onReorder(arrayMove(ids, from, to))
    }
  }
  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      modifiers={[restrictToVerticalAxis, restrictToParentElement]}
      onDragEnd={onDragEnd}
    >
      <SortableContext items={ids} strategy={verticalListSortingStrategy}>
        <div style={{ display: 'flex', flexDirection: 'column', gap }}>{children}</div>
      </SortableContext>
    </DndContext>
  )
}
