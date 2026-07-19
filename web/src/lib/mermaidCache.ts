export const MERMAID_RENDERER_VERSION = '11.16.0'

const pendingUploads = new Set<Promise<void>>()

export function cacheMermaidSVG(source: string, svg: string, theme: 'light' | 'dark'): Promise<void> {
  const upload = fetch('/api/mermaid-cache', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ source, svg, theme, version: MERMAID_RENDERER_VERSION }),
  })
    .then((response) => {
      if (!response.ok) throw new Error(`chart cache rejected (${response.status})`)
    })
    .catch(() => {
      // PDF caching is best-effort and must never replace a successfully rendered chart
      // with an error state on the reading page or in a streaming Chat response.
    })
    .finally(() => pendingUploads.delete(upload))

  pendingUploads.add(upload)
  return upload
}

export async function flushMermaidSVGCache(): Promise<void> {
  await Promise.allSettled(Array.from(pendingUploads))
}
