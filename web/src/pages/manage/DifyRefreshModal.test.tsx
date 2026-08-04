import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { App } from 'antd'
import DifyRefreshModal, { applicable } from './DifyRefreshModal'
import type { DifyRefreshResult } from '../../api/types'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, vars?: Record<string, unknown>) => (vars ? `${key}:${JSON.stringify(vars)}` : key) }),
}))

const base: DifyRefreshResult = { id: 1, local_name: '投资决策分析' }

const show = (results: DifyRefreshResult[]) =>
  render(
    <App>
      <DifyRefreshModal open results={results} onCancel={() => {}} onApply={async () => {}} />
    </App>,
  )

describe('applicable', () => {
  it('accepts a target that changed and came back with a list', () => {
    expect(applicable({ ...base, changed: true, added: ['rumor'], inputs: [{ variable: 'symbol' }, { variable: 'rumor' }] })).toBe(true)
  })

  // Nothing to write is not a thing to confirm.
  it('rejects an unchanged target', () => {
    expect(applicable({ ...base, changed: false, inputs: [{ variable: 'symbol' }] })).toBe(false)
  })

  it('rejects a target whose probe failed', () => {
    expect(applicable({ ...base, error: 'connect failed', changed: true })).toBe(false)
  })

  // An empty list from a failed /parameters means "we could not ask", not "there are no inputs".
  // Applying it would wipe whatever the admin has, including anything they typed in by hand.
  it('rejects a target whose parameter list did not answer', () => {
    expect(applicable({ ...base, inputs_error: 'boom', changed: true, inputs: [] })).toBe(false)
    expect(applicable({ ...base, changed: true, inputs: [] })).toBe(false)
  })
})

describe('DifyRefreshModal', () => {
  // The whole constraint of the feature: Dify's name is reference material, shown next to the
  // local one, and the copy says so rather than leaving an admin to guess.
  it("shows Dify's name as reference when it differs from the local one", () => {
    show([{ ...base, changed: true, name_differs: true, remote_name: 'Investment Decision', added: ['rumor'], inputs: [{ variable: 'rumor' }] }])
    expect(screen.getByText('batch.refresh.remoteName:{"name":"Investment Decision"}')).toBeTruthy()
    expect(screen.getByText('投资决策分析')).toBeTruthy()
  })

  it('does not mention a remote name when it matches', () => {
    show([{ ...base, changed: true, name_differs: false, remote_name: '投资决策分析', added: ['rumor'], inputs: [{ variable: 'rumor' }] }])
    expect(screen.queryByText(/batch\.refresh\.remoteName/)).toBeNull()
  })

  // Losing the stock-code input does not fail a run — it silently stops same-day reuse, so it gets
  // an alert rather than one more grey tag in a row of tags.
  it('raises an alert when the stock-code input is gone', () => {
    show([{ ...base, changed: true, removed: ['symbol'], symbol_input_lost: 'symbol', inputs: [{ variable: 'code' }] }])
    expect(screen.getByText('batch.refresh.symbolLost:{"name":"symbol"}')).toBeTruthy()
  })

  it('says so when every target already matches', () => {
    show([{ ...base, changed: false, inputs: [{ variable: 'symbol' }] }])
    expect(screen.getByText('batch.refresh.noChanges')).toBeTruthy()
  })

  // Applying nothing is not an action: with no applicable row the confirm is dead.
  it('disables the confirm when there is nothing to apply', () => {
    show([{ ...base, error: 'connect failed' }])
    const ok = screen.getByText('batch.refresh.apply:{"n":0}').closest('button')
    expect(ok?.disabled).toBe(true)
  })
})
