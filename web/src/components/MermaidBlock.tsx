import { useEffect, useRef, useState } from 'react'
import DOMPurify from 'dompurify'
import { loadMermaid } from '../lib/mermaid'

let nextChartID = 0
const MAX_MERMAID_SOURCE_SIZE = 50_000

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
  const [svg, setSVG] = useState('')
  const theme = useDocumentTheme()
  const id = useRef(`rp-mermaid-${(nextChartID += 1)}`)

  useEffect(() => {
    let active = true
    setSVG('')

    if (source.length > MAX_MERMAID_SOURCE_SIZE) return () => undefined

    void loadMermaid()
      .then(async ({ default: mermaid }) => {
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: 'strict',
          secure: ['securityLevel', 'startOnLoad', 'maxTextSize', 'maxEdges', 'suppressErrorRendering'],
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

  if (!svg) return fallback(source)
  return <div className="md-mermaid" dangerouslySetInnerHTML={{ __html: svg }} />
}
