import { act, render, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import Markdown from './Markdown'

const { renderMermaid, parseMermaid, initializeMermaid, loadMermaid } = vi.hoisted(() => {
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
  return { renderMermaid, parseMermaid, initializeMermaid, loadMermaid }
})

vi.mock('../lib/mermaid', () => ({
  loadMermaid,
}))

describe('Markdown', () => {
  afterEach(() => {
    document.documentElement.dataset.theme = 'light'
    renderMermaid.mockClear()
    parseMermaid.mockReset()
    parseMermaid.mockResolvedValue(true)
    initializeMermaid.mockClear()
    loadMermaid.mockClear()
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
    expect(initializeMermaid).toHaveBeenCalledWith(expect.objectContaining({ securityLevel: 'strict' }))
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
