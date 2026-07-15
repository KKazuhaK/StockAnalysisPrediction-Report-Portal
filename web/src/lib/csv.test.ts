import { describe, it, expect, vi } from 'vitest'
import { parseCSV, csvToRows, downloadCSV, toCSV } from './csv'

describe('parseCSV', () => {
  it('parses simple rows', () => {
    expect(parseCSV('a,b\n1,2')).toEqual([
      ['a', 'b'],
      ['1', '2'],
    ])
  })
  it('keeps commas inside quoted fields', () => {
    expect(parseCSV('code,rumor\n600519,"a, b, c"')).toEqual([
      ['code', 'rumor'],
      ['600519', 'a, b, c'],
    ])
  })
  it('unescapes doubled quotes', () => {
    expect(parseCSV('x\n"he said ""hi"""')).toEqual([['x'], ['he said "hi"']])
  })
  it('skips blank lines and trailing newline', () => {
    expect(parseCSV('a\n\n1\n')).toEqual([['a'], ['1']])
  })
})

describe('csvToRows', () => {
  it('maps header columns to keys, ignoring extras and filling missing', () => {
    expect(csvToRows('code,rumor,extra\n600519,merger,ignore', ['code', 'rumor'])).toEqual([
      { code: '600519', rumor: 'merger' },
    ])
  })
  it('handles reordered columns', () => {
    expect(csvToRows('rumor,code\nmerger,600519', ['code', 'rumor'])).toEqual([
      { code: '600519', rumor: 'merger' },
    ])
  })
  it('fills a missing column with empty string', () => {
    expect(csvToRows('code\n600519', ['code', 'rumor'])).toEqual([{ code: '600519', rumor: '' }])
  })
})

describe('toCSV', () => {
  it('quotes fields with commas', () => {
    expect(toCSV(['a', 'b'], [['x', 'y,z']])).toBe('a,b\nx,"y,z"')
  })
})

describe('downloadCSV', () => {
  it('creates and releases a CSV download URL', () => {
    const createObjectURL = vi.spyOn(URL, 'createObjectURL').mockReturnValue('blob:csv')
    const revokeObjectURL = vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => {})
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})

    downloadCSV('rows.csv', 'code\n600519')

    expect(createObjectURL).toHaveBeenCalledTimes(1)
    expect(click).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:csv')
    createObjectURL.mockRestore()
    revokeObjectURL.mockRestore()
    click.mockRestore()
  })
})
