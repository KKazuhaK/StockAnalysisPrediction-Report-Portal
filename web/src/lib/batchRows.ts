import type { PluginInput } from '../api/types'
import { parseCSV } from './csv'

export type BatchDraftRow = Record<string, string>

export interface MissingBatchCell {
  rowIndex: number
  key: string
}

export function blankBatchRow(inputs: PluginInput[]): BatchDraftRow {
  return Object.fromEntries(inputs.map((input) => [input.key, '']))
}

export function batchRowHasValues(row: BatchDraftRow): boolean {
  return Object.values(row).some((value) => String(value ?? '').trim() !== '')
}

export function activeBatchRows(rows: BatchDraftRow[]): BatchDraftRow[] {
  return rows.filter(batchRowHasValues)
}

export function missingRequiredCells(rows: BatchDraftRow[], inputs: PluginInput[]): MissingBatchCell[] {
  const required = inputs.filter((input) => input.required)
  const missing: MissingBatchCell[] = []
  rows.forEach((row, rowIndex) => {
    if (!batchRowHasValues(row)) return
    required.forEach((input) => {
      if (String(row[input.key] ?? '').trim() === '') missing.push({ rowIndex, key: input.key })
    })
  })
  return missing
}

function parseSpreadsheetGrid(text: string): string[][] {
  if (!text.includes('\t')) return parseCSV(text)
  return text
    .replace(/\r\n?/g, '\n')
    .split('\n')
    .map((line) => line.split('\t'))
    .filter((row) => row.some((cell) => cell !== ''))
}

function matchesDestinationHeader(row: string[], inputs: PluginInput[], startColumn: number): boolean {
  if (row.length === 0) return false
  return row.every((cell, offset) => cell.trim() === inputs[startColumn + offset]?.key)
}

export function pasteBatchGrid(
  rows: BatchDraftRow[],
  inputs: PluginInput[],
  startRow: number,
  startColumn: number,
  text: string,
): BatchDraftRow[] {
  let grid = parseSpreadsheetGrid(text)
  if (matchesDestinationHeader(grid[0] || [], inputs, startColumn)) grid = grid.slice(1)
  if (grid.length === 0) return rows

  const next = (rows.length > 0 ? rows : [blankBatchRow(inputs)]).map((row) => ({ ...blankBatchRow(inputs), ...row }))
  grid.forEach((cells, rowOffset) => {
    const rowIndex = startRow + rowOffset
    while (next.length <= rowIndex) next.push(blankBatchRow(inputs))
    cells.forEach((cell, columnOffset) => {
      const input = inputs[startColumn + columnOffset]
      if (input) next[rowIndex][input.key] = cell
    })
  })
  return next
}

export function missingCSVHeaders(text: string, inputs: PluginInput[]): string[] {
  const header = parseCSV(text)[0]?.map((cell) => cell.trim()) || []
  return inputs.map((input) => input.key).filter((key) => !header.includes(key))
}
