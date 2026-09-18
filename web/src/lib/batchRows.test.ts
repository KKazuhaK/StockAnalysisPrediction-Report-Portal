import { describe, expect, it } from 'vitest'

import {
  activeBatchRows,
  blankBatchRow,
  missingRequiredCells,
  missingCSVHeaders,
  pasteBatchGrid,
} from './batchRows'
import type { PluginInput } from '../api/types'

const inputs: PluginInput[] = [
  { key: 'symbol', required: true },
  { key: 'query', required: true },
  { key: 'context' },
]

describe('batch row editing', () => {
  it('creates a blank row with every declared column', () => {
    expect(blankBatchRow(inputs)).toEqual({ symbol: '', query: '', context: '' })
  })

  it('pastes a spreadsheet block into the selected cell and grows rows as needed', () => {
    expect(pasteBatchGrid([blankBatchRow(inputs)], inputs, 0, 0, '000001\talpha\n000002\tbeta')).toEqual([
      { symbol: '000001', query: 'alpha', context: '' },
      { symbol: '000002', query: 'beta', context: '' },
    ])
  })

  it('skips a pasted header row when it matches the destination columns', () => {
    expect(pasteBatchGrid([blankBatchRow(inputs)], inputs, 0, 0, 'symbol\tquery\n000001\talpha')).toEqual([
      { symbol: '000001', query: 'alpha', context: '' },
    ])
  })

  it('ignores placeholder rows and identifies missing required cells', () => {
    const rows = [blankBatchRow(inputs), { symbol: '000001', query: '', context: '' }]
    expect(activeBatchRows(rows)).toEqual([{ symbol: '000001', query: '', context: '' }])
    expect(missingRequiredCells(rows, inputs)).toEqual([{ rowIndex: 1, key: 'query' }])
  })

  it('reports columns missing from an imported CSV header', () => {
    expect(missingCSVHeaders('symbol,query\n000001,alpha', inputs)).toEqual(['context'])
  })
})
