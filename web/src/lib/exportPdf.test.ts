import { describe, it, expect } from 'vitest'
import { shouldDownloadPdf, buildPrintHtml } from './exportPdf'

describe('shouldDownloadPdf', () => {
  it('is true only when the server actually returned a PDF', () => {
    expect(shouldDownloadPdf({ ok: true, contentType: 'application/pdf' })).toBe(true)
    // wkhtmltopdf missing → 503 HTML error page → must fall back to print
    expect(shouldDownloadPdf({ ok: false, contentType: 'text/html; charset=utf-8' })).toBe(false)
    // 200 but not a PDF (defensive)
    expect(shouldDownloadPdf({ ok: true, contentType: 'text/html' })).toBe(false)
    expect(shouldDownloadPdf({ ok: true, contentType: null })).toBe(false)
  })
})

describe('buildPrintHtml', () => {
  it('embeds title + meta + body and auto-triggers the browser print dialog', () => {
    const doc = buildPrintHtml({
      title: '贵州茅台深度',
      date: '2026-05-09',
      source: '券商研报',
      html: '<h2>结论</h2><p>买入</p>',
    })
    expect(doc).toContain('贵州茅台深度')
    expect(doc).toContain('2026-05-09')
    expect(doc).toContain('<h2>结论</h2><p>买入</p>')
    expect(doc).toContain('window.print')
  })

  it('escapes the title (plain text) but preserves the body HTML', () => {
    const doc = buildPrintHtml({ title: '<script>x', date: '', source: '', html: '<p>ok</p>' })
    expect(doc).toContain('&lt;script&gt;x')
    expect(doc).not.toContain('<script>x')
    expect(doc).toContain('<p>ok</p>')
  })
})
