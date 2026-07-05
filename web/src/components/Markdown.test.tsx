import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import Markdown from './Markdown'

describe('Markdown', () => {
  it('renders report math blocks with KaTeX', () => {
    const md = String.raw`$$P_{基础} = \frac{4.7793}{4.7793 + 4.1289} = \frac{4.7793}{8.9082} = 53.65%$$`
    const { container } = render(<Markdown md={md} />)

    expect(container.querySelector('.katex-display')).not.toBeNull()
    expect(container.querySelector('math')).not.toBeNull()
    expect(container.textContent).toContain('基础')
    expect(container.querySelector('.katex-html')?.textContent).toContain('%')
  })
})
