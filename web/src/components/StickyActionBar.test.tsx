import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import StickyActionBar from './StickyActionBar'

describe('StickyActionBar', () => {
  it('renders its children', () => {
    render(
      <StickyActionBar>
        <button>save</button>
      </StickyActionBar>,
    )
    expect(screen.getByRole('button', { name: 'save' })).toBeTruthy()
  })

  it('pins itself to the bottom of the scroll area', () => {
    const { container } = render(
      <StickyActionBar>
        <button>save</button>
      </StickyActionBar>,
    )
    const bar = container.firstElementChild as HTMLElement
    expect(bar.style.position).toBe('sticky')
    expect(bar.style.bottom).toBe('0px')
  })
})
