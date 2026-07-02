import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Typography } from 'antd'

// Report body rendering: prefer markdown (react-markdown + GFM); fall back to direct rendering when an old report only has HTML.
export default function Markdown({ md, html }: { md?: string; html?: string }) {
  if (md && md.trim()) {
    return (
      <Typography>
        <div className="md-body">
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{md}</ReactMarkdown>
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
