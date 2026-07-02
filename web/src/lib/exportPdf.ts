// PDF export with graceful fallback: try the server (wkhtmltopdf, one-click
// download); if it's unavailable (e.g. not installed locally), fall back to the
// browser's native print / "Save as PDF" — zero dependency, works everywhere.

export interface ReportForPrint {
  title: string
  date?: string
  source?: string
  html?: string // rendered body HTML (preferred)
  md?: string // markdown fallback if no HTML
}

function esc(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// shouldDownloadPdf decides whether the server response is a real PDF (download
// it) or an error/HTML page (fall back to print).
export function shouldDownloadPdf(res: { ok: boolean; contentType: string | null }): boolean {
  return res.ok && !!res.contentType && res.contentType.includes('pdf')
}

// buildPrintHtml assembles a standalone, print-ready HTML document that mirrors
// the server-side pdf.html styling and auto-opens the print dialog on load.
export function buildPrintHtml({ title, date, source, html }: ReportForPrint): string {
  const meta = [date, source].filter(Boolean).join(' · ')
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>${esc(title)}</title>
<style>
  body { font-family: "Noto Sans CJK SC","Microsoft YaHei",sans-serif; font-size: 13px; line-height: 1.75; color:#1a1a1a; max-width: 820px; margin: 24px auto; padding: 0 16px; }
  h1 { font-size: 22px; color:#0c447c; border-bottom:2px solid #185fa5; padding-bottom:6px; }
  h2 { font-size: 17px; color:#185fa5; border-bottom:1px solid #d3d1c7; padding-bottom:4px; margin-top:18px; }
  h3 { font-size: 15px; }
  table { border-collapse: collapse; width:100%; margin:8px 0; font-size:12px; }
  th,td { border:1px solid #c9c7bd; padding:5px 8px; text-align:left; }
  th { background:#eef1f5; }
  blockquote { border-left:3px solid #ba7517; background:#faeeda; margin:8px 0; padding:5px 12px; }
  pre { background:#f7f6f1; border:1px solid #e1e0d9; padding:9px; white-space:pre-wrap; }
  img { max-width:100%; }
  @media print { body { margin: 0; } }
</style></head>
<body onload="window.print()">
<h1>${esc(title)}</h1>
${meta ? `<div style="color:#888;font-size:12px;margin-bottom:12px">${esc(meta)}</div>` : ''}
${html || ''}
</body></html>`
}

// printReport opens the print-ready document in a new window and prints it.
function printReport(report: ReportForPrint): void {
  const body = report.html && report.html.trim() ? report.html : `<pre>${esc(report.md || '')}</pre>`
  const doc = buildPrintHtml({ ...report, html: body })
  const win = window.open('', '_blank')
  if (!win) return // popup blocked; caller may notify
  win.document.open()
  win.document.write(doc)
  win.document.close()
}

function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// exportReportPdf tries the server first, then falls back to browser print.
export async function exportReportPdf(rid: string, report: ReportForPrint): Promise<void> {
  try {
    const res = await fetch(`/report/${encodeURIComponent(rid)}/pdf`, { credentials: 'same-origin' })
    if (shouldDownloadPdf({ ok: res.ok, contentType: res.headers.get('content-type') })) {
      const blob = await res.blob()
      const safe = (report.title || rid).replace(/[\\/:*?"<>|]+/g, '_').slice(0, 80)
      downloadBlob(blob, `${safe}.pdf`)
      return
    }
  } catch {
    // network error — fall through to print
  }
  printReport(report)
}
