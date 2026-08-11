import type { PluginInput } from '../api/types'

// How a run form turns a Dify workflow's declared inputs into the flat string row the batch API
// takes (POST /api/admin/batch/jobs, rows: [Record<string, string>]).
//
// Text, paragraph, number and select all end up as text, so the row shape did not have to change
// for them. Files did: a file lives on Dify, not in the row, so what travels is the id Dify gave
// it at upload time. Both file kinds send a JSON ARRAY of ids — the single-file `file` kind sends
// a one-element array rather than a bare id — so the backend has exactly one shape to parse and
// the declared type alone decides how it is spent.

// Dify's own ceilings: 15MB per file, 10 files per workflow run. Enforced in the browser so a
// file that can never be accepted is refused before it crosses the wire, not by a 400 afterwards.
export const MAX_FILE_MB = 15
export const MAX_FILE_BYTES = MAX_FILE_MB * 1024 * 1024
export const MAX_FILES = 10

// One file already uploaded to Dify. `id` is the only part a run carries; `uid` and `name` exist
// for the list the user sees, and `uid` is unique per upload so the same file picked twice still
// yields two removable entries.
export interface UploadedFile {
  uid: string
  name: string
  id: string
}

export function isFileInput(type?: string): boolean {
  return type === 'file' || type === 'file-list'
}

// fileLimit is how many files an input accepts: a `file-list` is capped by Dify's per-run ceiling,
// a `file` holds exactly one.
export function fileLimit(type?: string): number {
  return type === 'file-list' ? MAX_FILES : 1
}

// fileIds reads a file field's form value as the ids to submit. The value is whatever the upload
// control put there, so anything that is not a list of uploaded files reads as "nothing uploaded".
export function fileIds(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value
    .map((f) => (f && typeof f === 'object' ? String((f as UploadedFile).id ?? '') : ''))
    .filter((id) => id !== '')
}

// buildRow turns validated form values into the submitted row. A file input with nothing uploaded
// is left out entirely rather than sent as an empty array: absent is how the rest of the pipeline
// already reads an unfilled optional input, and an empty array would instead claim "run this with
// zero files", which is a different request.
export function buildRow(inputs: PluginInput[], vals: Record<string, unknown>): Record<string, string> {
  const row: Record<string, string> = {}
  for (const i of inputs) {
    if (isFileInput(i.type)) {
      const ids = fileIds(vals[i.key])
      if (ids.length > 0) row[i.key] = JSON.stringify(ids)
      continue
    }
    row[i.key] = String(vals[i.key] ?? '').trim()
  }
  return row
}
