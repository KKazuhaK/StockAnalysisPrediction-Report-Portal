import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { clickable } from './clickable'

// A card that is a clickable <div> is invisible to anyone not using a mouse. These pin the four
// things that make it a real control, because each is separately easy to drop.
describe('a clickable card', () => {
  const Card = ({ onOpen }: { onOpen: () => void }) => (
    <div {...clickable(onOpen, '比亚迪')}>
      <button onClick={(e) => e.stopPropagation()}>inner</button>
    </div>
  )

  it('is reachable, announced, and named', async () => {
    render(<Card onOpen={vi.fn()} />)
    const card = screen.getByRole('button', { name: '比亚迪' })
    expect(card.tabIndex).toBe(0)

    await userEvent.tab()
    expect(document.activeElement).toBe(card)
  })

  it('opens on Enter and on Space, not only on a click', async () => {
    const open = vi.fn()
    render(<Card onOpen={open} />)
    const card = screen.getByRole('button', { name: '比亚迪' })

    card.focus()
    await userEvent.keyboard('{Enter}')
    expect(open).toHaveBeenCalledTimes(1)
    await userEvent.keyboard(' ')
    expect(open).toHaveBeenCalledTimes(2)
    await userEvent.click(card)
    expect(open).toHaveBeenCalledTimes(3)
  })

  it('leaves other keys alone', async () => {
    const open = vi.fn()
    render(<Card onOpen={open} />)
    screen.getByRole('button', { name: '比亚迪' }).focus()
    await userEvent.keyboard('{Escape}{ArrowDown}a')
    expect(open).not.toHaveBeenCalled()
  })

  it('does not fire when a control inside the card handles the key itself', async () => {
    const open = vi.fn()
    render(<Card onOpen={open} />)
    screen.getByRole('button', { name: 'inner' }).focus()
    await userEvent.keyboard('{Enter}')
    // The inner button was activated; opening the card behind it would be a second, unasked action.
    expect(open).not.toHaveBeenCalled()
  })
})

// The label is optional, and getting that wrong is subtle: aria-label REPLACES the visible name
// rather than adding to it, so supplying one on an element that already shows its own text makes
// the control announce something different from what it reads.
describe('naming a clickable element', () => {
  it('lets its own text be the name when no label is given', () => {
    render(
      <a {...clickable(vi.fn())}>
        Show unchanged
      </a>,
    )
    expect(screen.getByRole('button', { name: 'Show unchanged' })).toBeTruthy()
  })

  it('takes a label when there is no text to read', () => {
    render(<img {...clickable(vi.fn(), '换一张')} alt="captcha" />)
    expect(screen.getByRole('button', { name: '换一张' })).toBeTruthy()
  })

  it('makes an href-less anchor focusable, which it is not on its own', async () => {
    const open = vi.fn()
    render(<a {...clickable(open)}>Open the report</a>)
    await userEvent.tab()
    const link = screen.getByRole('button', { name: 'Open the report' })
    expect(document.activeElement).toBe(link)
    await userEvent.keyboard('{Enter}')
    expect(open).toHaveBeenCalledTimes(1)
  })
})
