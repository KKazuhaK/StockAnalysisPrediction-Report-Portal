import { isValidElement, type ReactElement, type ReactNode } from 'react'
import ReactMarkdown from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import DOMPurify from 'dompurify'
import { Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import MermaidBlock from './MermaidBlock'
import 'katex/dist/katex.min.css'

type MarkdownAstNode = {
  type?: string
  value?: unknown
  lang?: string
  position?: {
    start?: { offset?: number }
    end?: { offset?: number }
  }
  data?: {
    hChildren?: MarkdownAstNode[]
    hProperties?: Record<string, unknown>
  }
  children?: MarkdownAstNode[]
}

function normalizeDisplayMath(md: string): string {
  let inFence = false
  return md
    .split('\n')
    .map((line) => {
      if (/^\s*(```|~~~)/.test(line)) {
        inFence = !inFence
        return line
      }
      if (inFence) return line

      const match = line.match(/^(\s*)\$\$\s*(\S[\s\S]*?)\s*\$\$(\s*)$/)
      if (!match) return line

      return `${match[1]}$$\n${match[2]}\n${match[1]}$$${match[3]}`
    })
    .join('\n')
}

function escapeBarePercents(value: string): string {
  let out = ''
  for (let i = 0; i < value.length; i += 1) {
    if (value[i] !== '%') {
      out += value[i]
      continue
    }

    let slashCount = 0
    for (let j = i - 1; j >= 0 && value[j] === '\\'; j -= 1) slashCount += 1
    if (slashCount % 2 === 0) out += '\\'
    out += '%'
  }
  return out
}

function remarkReportMathCompat() {
  return (tree: MarkdownAstNode) => {
    const visit = (node: MarkdownAstNode) => {
      if ((node.type === 'math' || node.type === 'inlineMath') && typeof node.value === 'string') {
        const next = escapeBarePercents(node.value)
        node.value = next
        replaceTextChildren(node.data?.hChildren, next)
      }
      node.children?.forEach(visit)
    }
    visit(tree)
  }
}

function replaceTextChildren(nodes: MarkdownAstNode[] | undefined, value: string) {
  nodes?.forEach((node) => {
    if (node.type === 'text') node.value = value
    replaceTextChildren(node.children, value)
  })
}

function remarkMermaidFenceState(source: string) {
  return (tree: MarkdownAstNode) => {
    const visit = (node: MarkdownAstNode) => {
      if (node.type === 'code' && node.lang?.toLowerCase() === 'mermaid') {
        const start = node.position?.start?.offset
        const end = node.position?.end?.offset
        const raw =
          typeof start === 'number' && typeof end === 'number'
            ? source.slice(start, end).replace(/[\r\n]+$/, '')
            : ''
        const lines = raw.split(/\r?\n/)
        const opening = lines[0]?.match(/^\s*(`{3,}|~{3,})/)
        const marker = opening?.[1]
        const closing = marker ? new RegExp(`^\\s*${marker[0]}{${marker.length},}\\s*$`) : null
        node.data ??= {}
        node.data.hProperties ??= {}
        node.data.hProperties['data-mermaid-closed'] = Boolean(closing?.test(lines[lines.length - 1] ?? ''))
      }
      node.children?.forEach(visit)
    }
    visit(tree)
  }
}

type CodeElementProps = {
  className?: string
  children?: ReactNode
  'data-mermaid-closed'?: boolean | string
}

function MarkdownPre({ children, ...props }: { children?: ReactNode }) {
  const code = isValidElement(children) ? (children as ReactElement<CodeElementProps>) : null
  const closed = code?.props['data-mermaid-closed'] === true || code?.props['data-mermaid-closed'] === 'true'
  if (closed && code?.props.className?.split(/\s+/).includes('language-mermaid')) {
    const source = String(code.props.children ?? '').replace(/\n$/, '')
    return <MermaidBlock source={source} />
  }
  return <pre {...props}>{children}</pre>
}

// Report body rendering: prefer markdown (react-markdown + GFM); fall back to direct rendering when an old report only has HTML.
export default function Markdown({ md, html }: { md?: string; html?: string }) {
  const { t } = useTranslation()
  if (md && md.trim()) {
    const normalized = normalizeDisplayMath(md)
    return (
      <Typography>
        <div className="md-body">
          <ReactMarkdown
            remarkPlugins={[remarkGfm, remarkMath, [remarkMermaidFenceState, normalized], remarkReportMathCompat]}
            rehypePlugins={[[rehypeKatex, { strict: false, throwOnError: false }]]}
            components={{
              pre: MarkdownPre,
              // Wrap tables so a wide one scrolls sideways instead of squishing columns
              // (which forces CJK headers to wrap one character per line, reading vertical).
              table: ({ node: _node, ...props }) => (
                <div className="md-table-wrap" role="region" aria-label={t('markdown.table')} tabIndex={0}>
                  <table {...props} />
                </div>
              ),
            }}
          >
            {normalized}
          </ReactMarkdown>
        </div>
      </Typography>
    )
  }
  if (html && html.trim()) {
    // Report bodies are externally ingested (Dify / legacy import), so the HTML is untrusted:
    // sanitize before injecting to strip <script>, inline event handlers (onerror/onload…), and
    // other XSS vectors. Otherwise a crafted body_html would run in an admin's same-origin session.
    return (
      <Typography>
        <div className="md-body" dangerouslySetInnerHTML={{ __html: DOMPurify.sanitize(html) }} />
      </Typography>
    )
  }
  return null
}
