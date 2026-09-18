import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { App } from 'antd'

import BatchRowsEditor from './BatchRowsEditor'
import type { PluginInput } from '../api/types'
import { blankBatchRow, type BatchDraftRow } from '../lib/batchRows'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, values?: Record<string, unknown>) => (values ? `${key}:${JSON.stringify(values)}` : key) }),
}))

const inputs: PluginInput[] = [
  { key: 'symbol', label: 'Stock code', required: true },
  { key: 'query', label: 'Research query', required: true },
  { key: 'context', label: 'Context', description: 'JSON object passed to the workflow' },
]

function Harness({ onValidityChange = () => {} }: { onValidityChange?: (valid: boolean) => void }) {
  const [rows, setRows] = useState<BatchDraftRow[]>([blankBatchRow(inputs)])
  return (
    <App>
      <BatchRowsEditor inputs={inputs} rows={rows} onChange={setRows} onValidityChange={onValidityChange} />
    </App>
  )
}

describe('BatchRowsEditor', () => {
  it('renders a bounded spreadsheet with compact field headings', () => {
    render(<Harness />)
    expect(document.body.querySelector('.rp-batch-grid')).toBeTruthy()
    expect(screen.getAllByText('Stock code').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Research query').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Context').length).toBeGreaterThan(0)
    expect(screen.getAllByText('run.optionalMark').length).toBeGreaterThan(0)
    expect(screen.getByRole('button', { name: /run\.inputHelp.*Context/ })).toBeTruthy()
  })

  it('accepts a multi-row spreadsheet paste starting at the focused cell', () => {
    render(<Harness />)
    const first = screen.getByRole('textbox', { name: /batch\.editor\.cellLabel.*Stock code/ })
    fireEvent.paste(first, { clipboardData: { getData: () => '000001\talpha\n000002\tbeta' } })

    expect(screen.getByDisplayValue('000001')).toBeTruthy()
    expect(screen.getByDisplayValue('alpha')).toBeTruthy()
    expect(screen.getByDisplayValue('000002')).toBeTruthy()
    expect(screen.getByDisplayValue('beta')).toBeTruthy()
  })

  it('keeps a synchronized CSV mode for bulk text editing', async () => {
    const user = userEvent.setup()
    render(<Harness />)
    await user.click(screen.getByText('batch.editor.csvMode'))

    const editor = screen.getByPlaceholderText(/batch\.csvPlaceholder/) as HTMLTextAreaElement
    expect(editor.value).toBe('symbol,query,context')
    await user.clear(editor)
    await user.type(editor, 'symbol,query,context{enter}000001,alpha,')
    expect(screen.getByText(/batch\.editor\.ready/)).toBeTruthy()
  })

  it('marks an incomplete required cell and only becomes valid after it is filled', async () => {
    const validity = vi.fn()
    const user = userEvent.setup()
    render(<Harness onValidityChange={validity} />)
    await user.type(screen.getByRole('textbox', { name: /Stock code/ }), '000001')

    expect(screen.getByText(/batch\.editor\.missingRequired/)).toBeTruthy()
    expect(validity).toHaveBeenLastCalledWith(false)

    await user.type(screen.getByRole('textbox', { name: /Research query/ }), 'alpha')
    expect(validity).toHaveBeenLastCalledWith(true)
  })
})
