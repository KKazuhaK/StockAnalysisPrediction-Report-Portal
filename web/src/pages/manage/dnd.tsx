import { createContext, useContext, useMemo, type ReactNode } from 'react'
import { Button } from 'antd'
import { HolderOutlined } from '@ant-design/icons'
import {
  DndContext,
  PointerSensor,
  closestCenter,
  useSensor,
  useSensors,
  type DragEndEvent,
} from '@dnd-kit/core'
import { restrictToParentElement, restrictToVerticalAxis } from '@dnd-kit/modifiers'
import { SortableContext, arrayMove, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'

// Drag-to-sort: wire antd Table body rows to @dnd-kit. Use a "drag handle" instead of dragging the
// whole row, to avoid conflicts with the input/dropdown inside a cell. See LinksPage / TypesPage for usage.

interface RowCtx {
  setActivatorNodeRef?: (el: HTMLElement | null) => void
  listeners?: Record<string, any>
}
const RowContext = createContext<RowCtx>({})

export function DragHandle() {
  const { setActivatorNodeRef, listeners } = useContext(RowContext)
  return (
    <Button
      type="text"
      size="small"
      icon={<HolderOutlined />}
      ref={setActivatorNodeRef}
      style={{ cursor: 'grab', touchAction: 'none' }}
      {...listeners}
    />
  )
}

export function SortableRow(props: React.HTMLAttributes<HTMLTableRowElement> & { 'data-row-key': string }) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({
    id: props['data-row-key'],
  })
  const style: React.CSSProperties = {
    ...props.style,
    transform: CSS.Translate.toString(transform),
    transition,
    ...(isDragging ? { position: 'relative', zIndex: 999 } : {}),
  }
  const ctx = useMemo<RowCtx>(() => ({ setActivatorNodeRef, listeners }), [setActivatorNodeRef, listeners])
  return (
    <RowContext.Provider value={ctx}>
      <tr {...props} ref={setNodeRef} style={style} {...attributes} />
    </RowContext.Provider>
  )
}

export const sortableTableComponents = { body: { row: SortableRow } }

// SortableWrapper provides DndContext + SortableContext; onReorder receives the reordered key sequence.
export function SortableWrapper({
  ids,
  onReorder,
  children,
}: {
  ids: string[]
  onReorder: (orderedIds: string[]) => void
  children: ReactNode
}) {
  const sensors = useSensors(useSensor(PointerSensor, { activationConstraint: { distance: 4 } }))
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
        {children}
      </SortableContext>
    </DndContext>
  )
}
