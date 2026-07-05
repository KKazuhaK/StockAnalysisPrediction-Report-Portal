import ReactMarkdown from 'react-markdown'
import rehypeKatex from 'rehype-katex'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import { Typography } from 'antd'
import 'katex/dist/katex.min.css'

type MarkdownAstNode = {
  type?: string
  value?: unknown
  data?: {
    hChildren?: MarkdownAstNode[]
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

// Report body rendering: prefer markdown (react-markdown + GFM); fall back to direct rendering when an old report only has HTML.
export default function Markdown({ md, html }: { md?: string; html?: string }) {
  if (md && md.trim()) {
    return (
      <Typography>
        <div className="md-body">
          <ReactMarkdown
            remarkPlugins={[remarkGfm, remarkMath, remarkReportMathCompat]}
            rehypePlugins={[[rehypeKatex, { strict: false, throwOnError: false }]]}
          >
            {normalizeDisplayMath(md)}
          </ReactMarkdown>
        </div>
      </Typography>
    )
  }
  if (html && html.trim()) {
    return (
      <Typography>
        <div className="md-body" dangerouslySetInnerHTML={{ __html: html }} />
      </Typography>
    )
  }
  return null
}
