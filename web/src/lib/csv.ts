// Shared CSV helpers for batch and recurring-run templates. parseCSV supports
// quoted fields (double quotes, "" escaping) so values may contain commas and
// newlines.

export function parseCSV(text: string): string[][] {
  const rows: string[][] = []
  let field = ''
  let row: string[] = []
  let inQuotes = false
  const pushField = () => {
    row.push(field)
    field = ''
  }
  const pushRow = () => {
    pushField()
    rows.push(row)
    row = []
  }
  for (let i = 0; i < text.length; i++) {
    const c = text[i]
    if (inQuotes) {
      if (c === '"') {
        if (text[i + 1] === '"') {
          field += '"'
          i++
        } else {
          inQuotes = false
        }
      } else {
        field += c
      }
      continue
    }
    if (c === '"') inQuotes = true
    else if (c === ',') pushField()
    else if (c === '\n') pushRow()
    else if (c === '\r') continue
    else field += c
  }
  if (field.length > 0 || row.length > 0) pushRow()
  // Drop rows that are entirely blank (e.g. a trailing newline).
  return rows.filter((r) => r.some((c) => c.trim() !== ''))
}

// csvToRows maps a parsed CSV (with a header row) to objects keyed by the expected
// input keys. Extra columns are ignored; missing columns become "". Column order
// is taken from the header, so reordered CSVs still map correctly.
export function csvToRows(text: string, keys: string[]): Record<string, string>[] {
  const grid = parseCSV(text)
  if (grid.length === 0) return []
  const header = grid[0].map((h) => h.trim())
  return grid.slice(1).map((cells) => {
    const obj: Record<string, string> = {}
    for (const k of keys) {
      const idx = header.indexOf(k)
      obj[k] = idx >= 0 && idx < cells.length ? cells[idx].trim() : ''
    }
    return obj
  })
}

// toCSV builds a CSV string, quoting fields that contain commas, quotes, or newlines.
export function toCSV(headers: string[], rows: (string | number)[][]): string {
  const esc = (v: string | number) => {
    const s = String(v ?? '')
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  return [headers, ...rows].map((r) => r.map(esc).join(',')).join('\n')
}

// downloadCSV saves an editor/template string as a UTF-8 CSV file in the browser.
export function downloadCSV(name: string, text: string): void {
  const blob = new Blob([text], { type: 'text/csv;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}
