import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { App } from 'antd'
import CompareModal from './CompareModal'

// What changed between two editions of the same analysis is the thing a recurring pipeline is
// actually producing, and reading two markdown documents side by side to find it is what nobody
// does. These pin the two decisions that make the dialog useful rather than merely present.

const apiMock = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }))
vi.mock('../api/client', () => ({
  api: apiMock,
  ApiError: class extends Error {},
  errText: (e: unknown) => String(e),
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k),
  }),
}))

const CANDIDATES = [
  { id: 11, date: '2026-06-30', title: '六月决策', version: '' },
  { id: 10, date: '2026-05-31', title: '五月决策', version: '' },
]

const DIFF = {
  a: { id: 11, title: '六月决策', date: '2026-06-30', symbol: '600519', name: '贵州茅台', rtype: '投资决策', version: '' },
  b: { id: 12, title: '七月决策', date: '2026-07-31', symbol: '600519', name: '贵州茅台', rtype: '投资决策', version: '' },
  changed: 1,
  sections: [
    { heading: '结论', level: 1, status: 'same' as const },
    {
      heading: '估值',
      level: 2,
      status: 'changed' as const,
      lines: [
        { op: '-' as const, text: '目标价 48 元' },
        { op: '+' as const, text: '目标价 55 元' },
        { op: ' ' as const, text: '维持' },
      ],
    },
  ],
}

const mount = () =>
  render(
    <App>
      <CompareModal reportId={12} open onClose={vi.fn()} />
    </App>,
  )

describe('CompareModal', () => {
  beforeEach(() => {
    apiMock.get.mockReset()
    apiMock.get.mockImplementation((url: string) =>
      url.startsWith('/api/reports/comparable')
        ? Promise.resolve({ items: CANDIDATES })
        : Promise.resolve(DIFF),
    )
  })

  // Defaulting to the previous edition is the whole ergonomics of it: the comparison people want
  // should need no choosing.
  it('defaults to the most recent earlier edition and diffs against it', async () => {
    mount()
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledWith('/api/reports/diff?a=11&b=12'))
    // Asserted against the whole dialog's text rather than element-by-element. A diff line renders
    // as <span>-</span> plus a text node, so no single element's textContent equals the line, and
    // an element query over that has to guess which ancestor to match — which it did differently
    // depending on how far the modal's enter animation had progressed.
    await waitFor(() => {
      const text = document.body.textContent ?? ''
      expect(text).toContain('目标价 55 元') // the newer edition
      expect(text).toContain('目标价 48 元') // and what it replaced
    })
  })

  // Unchanged sections are most of a long report; leading with them would bury the news.
  it('hides unchanged sections until asked', async () => {
    mount()
    expect(await screen.findByText('估值')).toBeTruthy()
    expect(screen.queryByText('结论')).toBeNull()

    fireEvent.click(screen.getByText('compare.showUnchanged'))
    expect(await screen.findByText('结论')).toBeTruthy()
  })

  it('keeps the unchanged lines of a changed section as context', async () => {
    mount()
    // Without them a reader cannot see WHERE in the section the change is.
    expect(await screen.findByText(/维持/)).toBeTruthy()
  })

  it('says so plainly when there is nothing to compare against', async () => {
    apiMock.get.mockImplementation((url: string) =>
      url.startsWith('/api/reports/comparable') ? Promise.resolve({ items: [] }) : Promise.resolve(DIFF),
    )
    mount()
    expect(await screen.findByText('compare.noneToCompare')).toBeTruthy()
    // And does not ask the server to diff against nothing.
    await waitFor(() => expect(apiMock.get).toHaveBeenCalledTimes(1))
  })
})
