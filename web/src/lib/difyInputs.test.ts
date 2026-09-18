import { describe, expect, it } from 'vitest'

import { MAX_FILES, buildRow, fileIds, fileLimit, inputDisplayMeta, isFileInput } from './difyInputs'
import type { PluginInput } from '../api/types'

const decl = (key: string, type?: string): PluginInput => ({ key, type })

describe('inputDisplayMeta', () => {
  it('uses a trailing parenthetical note as a compatibility fallback', () => {
    expect(inputDisplayMeta({ key: 'date', label: 'Report date (optional, parent supplied)' })).toEqual({
      title: 'Report date',
      detail: 'parent supplied',
    })
  })

  it('supports full-width parentheses without depending on a field key', () => {
    expect(inputDisplayMeta({ key: 'cutoff', label: 'Cutoff \uFF08ISO 8601\uFF09' })).toEqual({
      title: 'Cutoff',
      detail: 'ISO 8601',
    })
  })

  it('removes a localized optional marker while retaining the actual legacy help', () => {
    expect(inputDisplayMeta({
      key: 'policy',
      label: 'Policy JSON \uFF08\u53EF\u9009\uFF0Cuse defaults when empty\uFF09',
    })).toEqual({
      title: 'Policy JSON',
      detail: 'use defaults when empty',
    })
  })

  it('keeps an explicit description authoritative', () => {
    expect(inputDisplayMeta({ key: 'date', label: 'Report date (source label)', description: 'Parent supplied' })).toEqual({
      title: 'Report date (source label)',
      detail: 'Parent supplied',
    })
  })

  it('shortens a note that only repeats optional state without inventing help', () => {
    expect(inputDisplayMeta({ key: 'context', label: 'Execution context (optional)' })).toEqual({
      title: 'Execution context',
      detail: '',
    })
  })
})

describe('isFileInput / fileLimit', () => {
  it('recognises the two file kinds and nothing else', () => {
    expect(isFileInput('file')).toBe(true)
    expect(isFileInput('file-list')).toBe(true)
    for (const ty of ['text-input', 'paragraph', 'number', 'select', undefined]) {
      expect(isFileInput(ty), String(ty)).toBe(false)
    }
  })

  it('caps a list at the per-run ceiling and a single file at one', () => {
    expect(fileLimit('file-list')).toBe(MAX_FILES)
    expect(fileLimit('file')).toBe(1)
  })
})

describe('fileIds', () => {
  it('reads the ids out of uploaded files, in order', () => {
    expect(fileIds([{ uid: 'a-1', name: 'a.pdf', id: 'a' }, { uid: 'b-2', name: 'b.pdf', id: 'b' }])).toEqual(['a', 'b'])
  })

  it('treats anything that is not a list of uploaded files as nothing uploaded', () => {
    for (const v of [undefined, null, '', 'abc', 42, {}, [null], [{ uid: 'x', name: 'x' }]]) {
      expect(fileIds(v), JSON.stringify(v) ?? 'undefined').toEqual([])
    }
  })
})

describe('buildRow', () => {
  it('sends text inputs as trimmed strings, whatever type produced them', () => {
    const inputs = [decl('symbol'), decl('note', 'paragraph'), decl('n', 'number'), decl('mode', 'select')]
    expect(buildRow(inputs, { symbol: '  301539 ', note: 'hello', n: 7, mode: 'fast' })).toEqual({
      symbol: '301539',
      note: 'hello',
      n: '7',
      mode: 'fast',
    })
  })

  it('keeps an unfilled text input as an empty string, so the row still declares it', () => {
    expect(buildRow([decl('symbol')], {})).toEqual({ symbol: '' })
  })

  it('sends a file-list as a JSON array of Dify file ids', () => {
    const vals = { docs: [{ uid: 'a-1', name: 'a.pdf', id: 'a' }, { uid: 'b-2', name: 'b.pdf', id: 'b' }] }
    expect(buildRow([decl('docs', 'file-list')], vals)).toEqual({ docs: '["a","b"]' })
  })

  it('sends a single file as a one-element array, not a bare id', () => {
    // One shape for both kinds: the declared type decides how the backend spends it, never the
    // shape of the value it finds.
    const vals = { doc: [{ uid: 'a-1', name: 'a.pdf', id: 'a' }] }
    expect(buildRow([decl('doc', 'file')], vals)).toEqual({ doc: '["a"]' })
  })

  it('leaves a file input out entirely when nothing was uploaded', () => {
    // Not `""` and not `"[]"`: an empty array would read as "run this with zero files", which is a
    // claim, where an absent key is the same nothing an unfilled optional input has always been.
    const row = buildRow([decl('symbol'), decl('docs', 'file-list')], { symbol: '301539' })
    expect(row).toEqual({ symbol: '301539' })
    expect('docs' in row).toBe(false)
  })
})
