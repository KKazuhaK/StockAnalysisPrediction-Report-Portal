import {
  useEffect,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type WheelEvent,
} from 'react'
import DOMPurify from 'dompurify'
import { useTranslation } from 'react-i18next'
import { loadMermaid } from '../lib/mermaid'

let nextChartID = 0
const MAX_MERMAID_SOURCE_SIZE = 50_000
const MIN_SCALE = 0.5
const MAX_SCALE = 4
const BUTTON_SCALE_STEP = 0.25
const WHEEL_SCALE_FACTOR = 1.1

interface ChartView {
  scale: number
  x: number
  y: number
}

interface DragState {
  pointerID: number
  startX: number
  startY: number
  originX: number
  originY: number
}

const DEFAULT_VIEW: ChartView = { scale: 1, x: 0, y: 0 }

function clampScale(scale: number) {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, Math.round(scale * 100) / 100))
}

function currentTheme(): 'light' | 'dark' {
  return document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light'
}

function useDocumentTheme() {
  const [theme, setTheme] = useState(currentTheme)

  useEffect(() => {
    const observer = new MutationObserver(() => setTheme(currentTheme()))
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    return () => observer.disconnect()
  }, [])

  return theme
}

function fallback(source: string) {
  return (
    <pre>
      <code className="language-mermaid">{source}</code>
    </pre>
  )
}

export default function MermaidBlock({ source }: { source: string }) {
  const { t } = useTranslation()
  const [svg, setSVG] = useState('')
  const [view, setView] = useState<ChartView>(DEFAULT_VIEW)
  const [dragging, setDragging] = useState(false)
  const theme = useDocumentTheme()
  const id = useRef(`rp-mermaid-${(nextChartID += 1)}`)
  const viewportRef = useRef<HTMLDivElement>(null)
  const dragRef = useRef<DragState | null>(null)

  useEffect(() => {
    let active = true
    setSVG('')
    setView(DEFAULT_VIEW)
    dragRef.current = null
    setDragging(false)

    if (source.length > MAX_MERMAID_SOURCE_SIZE) return () => undefined

    void loadMermaid()
      .then(async ({ default: mermaid }) => {
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          htmlLabels: false,
          secure: [
            'securityLevel',
            'startOnLoad',
            'htmlLabels',
            'maxTextSize',
            'maxEdges',
            'suppressErrorRendering',
          ],
          maxTextSize: MAX_MERMAID_SOURCE_SIZE,
          maxEdges: 500,
          theme: theme === 'dark' ? 'dark' : 'default',
          suppressErrorRendering: true,
        })
        const valid = await mermaid.parse(source, { suppressErrors: true })
        if (!valid || !active) return
        const rendered = await mermaid.render(id.current, source)
        if (!active) return
        setSVG(
          DOMPurify.sanitize(rendered.svg, {
            USE_PROFILES: { svg: true, svgFilters: true },
            FORBID_TAGS: ['foreignObject', 'script'],
            FORBID_ATTR: ['onerror', 'onload', 'onclick'],
          }),
        )
      })
      .catch(() => {
        if (active) setSVG('')
      })

    return () => {
      active = false
    }
  }, [source, theme])

  const changeScale = (nextScale: (current: number) => number, originX?: number, originY?: number) => {
    const rect = viewportRef.current?.getBoundingClientRect()
    const pivotX = originX ?? (rect?.width ?? 0) / 2
    const pivotY = originY ?? (rect?.height ?? 0) / 2

    setView((current) => {
      const scale = clampScale(nextScale(current.scale))
      const ratio = scale / current.scale
      return {
        scale,
        x: pivotX - (pivotX - current.x) * ratio,
        y: pivotY - (pivotY - current.y) * ratio,
      }
    })
  }

  const handleWheel = (event: WheelEvent<HTMLDivElement>) => {
    event.preventDefault()
    const rect = event.currentTarget.getBoundingClientRect()
    const factor = event.deltaY < 0 ? WHEEL_SCALE_FACTOR : 1 / WHEEL_SCALE_FACTOR
    changeScale((scale) => scale * factor, event.clientX - rect.left, event.clientY - rect.top)
  }

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    dragRef.current = {
      pointerID: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      originX: view.x,
      originY: view.y,
    }
    event.currentTarget.setPointerCapture?.(event.pointerId)
    setDragging(true)
  }

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    const drag = dragRef.current
    if (!drag || drag.pointerID !== event.pointerId) return
    setView((current) => ({
      ...current,
      x: drag.originX + event.clientX - drag.startX,
      y: drag.originY + event.clientY - drag.startY,
    }))
  }

  const endDrag = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (dragRef.current?.pointerID !== event.pointerId) return
    dragRef.current = null
    if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
    setDragging(false)
  }

  if (!svg) return fallback(source)
  return (
    <div className="md-mermaid">
      <div className="md-mermaid-toolbar" role="toolbar" aria-label={t('mermaid.controls')}>
        <span className="md-mermaid-hint">{t('mermaid.hint')}</span>
        <button
          type="button"
          className="md-mermaid-control md-mermaid-zoom-out"
          aria-label={t('mermaid.zoomOut')}
          title={t('mermaid.zoomOut')}
          disabled={view.scale <= MIN_SCALE}
          onClick={() => changeScale((scale) => scale - BUTTON_SCALE_STEP)}
        >
          <span aria-hidden>−</span>
        </button>
        <output className="md-mermaid-scale" aria-label={t('mermaid.zoomLevel')} aria-live="polite">
          {Math.round(view.scale * 100)}%
        </output>
        <button
          type="button"
          className="md-mermaid-control md-mermaid-zoom-in"
          aria-label={t('mermaid.zoomIn')}
          title={t('mermaid.zoomIn')}
          disabled={view.scale >= MAX_SCALE}
          onClick={() => changeScale((scale) => scale + BUTTON_SCALE_STEP)}
        >
          <span aria-hidden>+</span>
        </button>
        <button
          type="button"
          className="md-mermaid-control md-mermaid-reset"
          aria-label={t('mermaid.reset')}
          title={t('mermaid.reset')}
          onClick={() => setView(DEFAULT_VIEW)}
        >
          <span aria-hidden>1:1</span>
        </button>
      </div>
      <div
        ref={viewportRef}
        className={`md-mermaid-viewport${dragging ? ' is-dragging' : ''}`}
        role="region"
        aria-label={t('mermaid.viewport')}
        onWheel={handleWheel}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
        onLostPointerCapture={endDrag}
      >
        <div
          className="md-mermaid-canvas"
          style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
          dangerouslySetInnerHTML={{ __html: svg }}
        />
      </div>
    </div>
  )
}
