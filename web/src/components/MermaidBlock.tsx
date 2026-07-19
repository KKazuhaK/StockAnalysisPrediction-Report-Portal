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
import { flattenMermaidSVG } from '../lib/flattenSvg'
import { cacheMermaidSVG } from '../lib/mermaidCache'

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

interface PointerPoint {
  x: number
  y: number
}

interface DragGesture {
  kind: 'drag'
  pointerID: number
  startX: number
  startY: number
  originX: number
  originY: number
}

interface PinchGesture {
  kind: 'pinch'
  pointerIDs: [number, number]
  startDistance: number
  startCenter: PointerPoint
  startView: ChartView
}

type ChartGesture = DragGesture | PinchGesture

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
  const canvasRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<ChartView>(DEFAULT_VIEW)
  const pointersRef = useRef(new Map<number, PointerPoint>())
  const gestureRef = useRef<ChartGesture | null>(null)

  const updateView = (updater: ChartView | ((current: ChartView) => ChartView)) => {
    setView((current) => {
      const next = typeof updater === 'function' ? updater(current) : updater
      viewRef.current = next
      return next
    })
  }

  useEffect(() => {
    let active = true
    setSVG('')
    updateView(DEFAULT_VIEW)
    pointersRef.current.clear()
    gestureRef.current = null
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

  useEffect(() => {
    if (!svg) return
    const renderedSVG = canvasRef.current?.querySelector('svg')
    if (!(renderedSVG instanceof SVGSVGElement)) return
    void cacheMermaidSVG(source, flattenMermaidSVG(renderedSVG), theme)
  }, [source, svg, theme])

  const changeScale = (nextScale: (current: number) => number, originX?: number, originY?: number) => {
    const rect = viewportRef.current?.getBoundingClientRect()
    const pivotX = originX ?? (rect?.width ?? 0) / 2
    const pivotY = originY ?? (rect?.height ?? 0) / 2

    updateView((current) => {
      const scale = clampScale(nextScale(current.scale))
      const ratio = scale / current.scale
      return {
        scale,
        x: pivotX - (pivotX - current.x) * ratio,
        y: pivotY - (pivotY - current.y) * ratio,
      }
    })
  }

  const localPoint = (event: ReactPointerEvent<HTMLDivElement>): PointerPoint => {
    const rect = event.currentTarget.getBoundingClientRect()
    return { x: event.clientX - rect.left, y: event.clientY - rect.top }
  }

  const pointerPair = (ids: [number, number]): [PointerPoint, PointerPoint] | null => {
    const first = pointersRef.current.get(ids[0])
    const second = pointersRef.current.get(ids[1])
    return first && second ? [first, second] : null
  }

  const pointDistance = (first: PointerPoint, second: PointerPoint) =>
    Math.hypot(second.x - first.x, second.y - first.y)

  const pointCenter = (first: PointerPoint, second: PointerPoint): PointerPoint => ({
    x: (first.x + second.x) / 2,
    y: (first.y + second.y) / 2,
  })

  const handleWheel = (event: WheelEvent<HTMLDivElement>) => {
    event.preventDefault()
    const rect = event.currentTarget.getBoundingClientRect()
    const factor = event.deltaY < 0 ? WHEEL_SCALE_FACTOR : 1 / WHEEL_SCALE_FACTOR
    changeScale((scale) => scale * factor, event.clientX - rect.left, event.clientY - rect.top)
  }

  const handlePointerDown = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    const point = localPoint(event)
    pointersRef.current.set(event.pointerId, point)
    event.currentTarget.setPointerCapture?.(event.pointerId)
    setDragging(true)

    const ids = Array.from(pointersRef.current.keys())
    if (ids.length >= 2) {
      const pointerIDs: [number, number] = [ids[0], ids[1]]
      const pair = pointerPair(pointerIDs)
      if (!pair) return
      gestureRef.current = {
        kind: 'pinch',
        pointerIDs,
        startDistance: Math.max(1, pointDistance(pair[0], pair[1])),
        startCenter: pointCenter(pair[0], pair[1]),
        startView: viewRef.current,
      }
      return
    }

    gestureRef.current = {
      kind: 'drag',
      pointerID: event.pointerId,
      startX: point.x,
      startY: point.y,
      originX: viewRef.current.x,
      originY: viewRef.current.y,
    }
  }

  const handlePointerMove = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (!pointersRef.current.has(event.pointerId)) return
    const point = localPoint(event)
    pointersRef.current.set(event.pointerId, point)
    const gesture = gestureRef.current
    if (!gesture) return

    if (gesture.kind === 'pinch') {
      const pair = pointerPair(gesture.pointerIDs)
      if (!pair) return
      const center = pointCenter(pair[0], pair[1])
      const scale = clampScale(
        gesture.startView.scale * (pointDistance(pair[0], pair[1]) / gesture.startDistance),
      )
      const chartX = (gesture.startCenter.x - gesture.startView.x) / gesture.startView.scale
      const chartY = (gesture.startCenter.y - gesture.startView.y) / gesture.startView.scale
      updateView({
        scale,
        x: center.x - chartX * scale,
        y: center.y - chartY * scale,
      })
      return
    }

    if (gesture.pointerID !== event.pointerId) return
    updateView((current) => ({
      ...current,
      x: gesture.originX + point.x - gesture.startX,
      y: gesture.originY + point.y - gesture.startY,
    }))
  }

  const endDrag = (event: ReactPointerEvent<HTMLDivElement>) => {
    pointersRef.current.delete(event.pointerId)
    if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId)
    }
    const remaining = Array.from(pointersRef.current.entries())
    if (remaining.length === 1) {
      const [pointerID, point] = remaining[0]
      gestureRef.current = {
        kind: 'drag',
        pointerID,
        startX: point.x,
        startY: point.y,
        originX: viewRef.current.x,
        originY: viewRef.current.y,
      }
      return
    }
    gestureRef.current = null
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
          onClick={() => updateView(DEFAULT_VIEW)}
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
          ref={canvasRef}
          className="md-mermaid-canvas"
          style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.scale})` }}
          dangerouslySetInnerHTML={{ __html: svg }}
        />
      </div>
    </div>
  )
}
