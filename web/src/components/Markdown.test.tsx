import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Markdown from './Markdown'

const { renderMermaid, parseMermaid, initializeMermaid, loadMermaid, cacheMermaidSVG } = vi.hoisted(() => {
  const renderMermaid = vi.fn(async (id: string) => ({ svg: `<svg data-chart-id="${id}"><text>chart</text></svg>` }))
  const parseMermaid = vi.fn(async () => true)
  const initializeMermaid = vi.fn()
  const loadMermaid = vi.fn(async () => ({
    default: {
      initialize: initializeMermaid,
      parse: parseMermaid,
      render: renderMermaid,
    },
  }))
  const cacheMermaidSVG = vi.fn(async () => undefined)
  return { renderMermaid, parseMermaid, initializeMermaid, loadMermaid, cacheMermaidSVG }
})

vi.mock('../lib/mermaid', () => ({
  loadMermaid,
}))

vi.mock('../lib/mermaidCache', () => ({
  cacheMermaidSVG,
}))

vi.mock('../lib/flattenSvg', () => ({
  flattenMermaidSVG: () => '<svg><text>flattened</text></svg>',
}))

describe('Markdown', () => {
  afterEach(() => {
    document.documentElement.dataset.theme = 'light'
    renderMermaid.mockClear()
    parseMermaid.mockReset()
    parseMermaid.mockResolvedValue(true)
    initializeMermaid.mockClear()
    loadMermaid.mockClear()
    cacheMermaidSVG.mockClear()
  })

  it('renders report math blocks with KaTeX', () => {
    const md = String.raw`$$P_{基础} = \frac{4.7793}{4.7793 + 4.1289} = \frac{4.7793}{8.9082} = 53.65%$$`
    const { container } = render(<Markdown md={md} />)

    expect(container.querySelector('.katex-display')).not.toBeNull()
    expect(container.querySelector('math')).not.toBeNull()
    expect(container.textContent).toContain('基础')
    expect(container.querySelector('.katex-html')?.textContent).toContain('%')
  })

  it('sanitizes the HTML fallback: strips scripts and inline event handlers', () => {
    const html = '<p>ok</p><img src=x onerror="window.__xss=1"><script>window.__xss=1</script>'
    const { container } = render(<Markdown html={html} />)
    // benign content survives...
    expect(container.textContent).toContain('ok')
    // ...but the script tag and the onerror handler are removed.
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('img')?.getAttribute('onerror')).toBeNull()
    expect(container.innerHTML).not.toContain('onerror')
  })

  it('renders a mermaid fence as an SVG instead of source code', async () => {
    const { container } = render(<Markdown md={'```mermaid\nxychart-beta\n  line [1, 2]\n```'} />)

    await waitFor(() => expect(container.querySelector('svg')).not.toBeNull())
    expect(container.textContent).toContain('chart')
    expect(container.textContent).not.toContain('xychart-beta')
    expect(initializeMermaid).toHaveBeenCalledWith(
      expect.objectContaining({
        securityLevel: 'strict',
        htmlLabels: false,
        secure: expect.arrayContaining(['htmlLabels']),
      }),
    )
    expect(cacheMermaidSVG).toHaveBeenCalledWith(
      expect.stringContaining('xychart-beta'),
      '<svg><text>flattened</text></svg>',
      'light',
    )
  })

  it('zooms, pans, and resets a rendered mermaid chart', async () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)

    await waitFor(() => expect(container.querySelector('.md-mermaid svg')).not.toBeNull())
    const viewport = container.querySelector<HTMLElement>('.md-mermaid-viewport')!
    const canvas = container.querySelector<HTMLElement>('.md-mermaid-canvas')!
    const zoomIn = container.querySelector<HTMLButtonElement>('.md-mermaid-zoom-in')!
    const reset = container.querySelector<HTMLButtonElement>('.md-mermaid-reset')!

    fireEvent.click(zoomIn)
    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(1.25)')

    fireEvent.pointerDown(viewport, { pointerId: 1, button: 0, clientX: 10, clientY: 20 })
    fireEvent.pointerMove(viewport, { pointerId: 1, clientX: 50, clientY: 80 })
    fireEvent.pointerUp(viewport, { pointerId: 1 })
    expect(canvas.style.transform).toBe('translate(40px, 60px) scale(1.25)')

    fireEvent.click(reset)
    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(1)')

    fireEvent.wheel(viewport, { deltaY: -100, clientX: 0, clientY: 0 })
    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(1.1)')
  })

  it('pinch-zooms a rendered mermaid chart on touch devices', async () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)

    await waitFor(() => expect(container.querySelector('.md-mermaid svg')).not.toBeNull())
    const viewport = container.querySelector<HTMLElement>('.md-mermaid-viewport')!
    const canvas = container.querySelector<HTMLElement>('.md-mermaid-canvas')!

    fireEvent.pointerDown(viewport, { pointerId: 1, pointerType: 'touch', button: 0, clientX: 0, clientY: 0 })
    fireEvent.pointerDown(viewport, { pointerId: 2, pointerType: 'touch', button: 0, clientX: 100, clientY: 0 })
    fireEvent.pointerMove(viewport, { pointerId: 2, pointerType: 'touch', clientX: 200, clientY: 0 })

    expect(canvas.style.transform).toBe('translate(0px, 0px) scale(2)')
  })

  it('exposes wide tables as keyboard-scrollable regions', () => {
    const { container } = render(<Markdown md={'| A | B | C |\n|---|---|---|\n| 1 | 2 | 3 |'} />)

    const wrap = container.querySelector<HTMLElement>('.md-table-wrap')!
    expect(wrap.getAttribute('role')).toBe('region')
    expect(wrap.getAttribute('tabindex')).toBe('0')
    expect(wrap.querySelector('table')).not.toBeNull()
  })

  it('renders ordinary code fences unchanged', () => {
    const { container } = render(<Markdown md={'```typescript\nconst answer = 42\n```'} />)

    expect(container.querySelector('code.language-typescript')?.textContent).toContain('const answer = 42')
    expect(loadMermaid).not.toHaveBeenCalled()
    expect(renderMermaid).not.toHaveBeenCalled()
  })

  it('does not load mermaid until a streaming fence is closed', () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B'} />)

    expect(container.querySelector('svg')).toBeNull()
    expect(container.querySelector('code.language-mermaid')?.textContent).toContain('flowchart LR')
    expect(loadMermaid).not.toHaveBeenCalled()
    expect(parseMermaid).not.toHaveBeenCalled()
  })

  it('keeps invalid mermaid source as a code block', async () => {
    parseMermaid.mockResolvedValueOnce(false)
    const { container } = render(<Markdown md={'```mermaid\nxychart-beta\n  line ['} />)

    expect(container.querySelector('svg')).toBeNull()
    expect(container.querySelector('code.language-mermaid')?.textContent).toContain('xychart-beta')
    expect(loadMermaid).not.toHaveBeenCalled()
    expect(parseMermaid).not.toHaveBeenCalled()
    expect(renderMermaid).not.toHaveBeenCalled()
  })

  it('keeps invalid source as code after its fence closes', async () => {
    parseMermaid.mockResolvedValueOnce(false)
    const { container } = render(<Markdown md={'```mermaid\nxychart-beta\n  line [\n```'} />)

    await waitFor(() => expect(parseMermaid).toHaveBeenCalled())
    expect(container.querySelector('svg')).toBeNull()
    expect(container.querySelector('code.language-mermaid')).not.toBeNull()
    expect(renderMermaid).not.toHaveBeenCalled()
  })

  it('does not load mermaid for an oversized fence', () => {
    const source = `flowchart LR\n${'A --> B\n'.repeat(7_000)}`
    const { container } = render(<Markdown md={`\`\`\`mermaid\n${source}\`\`\``} />)

    expect(container.querySelector('code.language-mermaid')).not.toBeNull()
    expect(loadMermaid).not.toHaveBeenCalled()
  })

  it('sanitizes the SVG returned by mermaid before inserting it', async () => {
    renderMermaid.mockResolvedValueOnce({
      svg: '<svg onload="alert(1)"><script>alert(1)</script><foreignObject>unsafe</foreignObject><text>safe</text></svg>',
    })
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)

    await waitFor(() => expect(container.querySelector('.md-mermaid svg')).not.toBeNull())
    expect(container.querySelector('script')).toBeNull()
    expect(container.querySelector('foreignObject')).toBeNull()
    expect(container.querySelector('svg')?.getAttribute('onload')).toBeNull()
    expect(container.textContent).toContain('safe')
  })

  it('rerenders mermaid charts when the document theme changes', async () => {
    const { container } = render(<Markdown md={'```mermaid\nflowchart LR\nA --> B\n```'} />)
    await waitFor(() => expect(renderMermaid).toHaveBeenCalledTimes(1))

    await act(async () => {
      document.documentElement.dataset.theme = 'dark'
      await Promise.resolve()
    })

    await waitFor(() => expect(renderMermaid).toHaveBeenCalledTimes(2))
    expect(container.querySelector('svg')).not.toBeNull()
  })
})
