import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import TimelinePanel from './TimelinePanel'

const nodes = [
  { date: '2026-05-09', n: 3 },
  { date: '2026-04-20', n: 7 },
  { date: '2026-01-17', n: 1 },
]

describe('TimelinePanel (vertical / desktop)', () => {
  it('renders a bounded scroll container so a long timeline does not grow the page', () => {
    render(<TimelinePanel nodes={nodes} selected="2026-05-09" onSelect={() => {}} horizontal={false} />)
    const scroll = screen.getByTestId('timeline-scroll')
    expect(scroll.style.overflowY).toBe('auto')
    expect(scroll.style.maxHeight).not.toBe('')
  })

  it('shows every date and fires onSelect when one is clicked', () => {
    const onSelect = vi.fn()
    render(<TimelinePanel nodes={nodes} selected="2026-05-09" onSelect={onSelect} horizontal={false} />)
    nodes.forEach((n) => expect(screen.getByText(n.date)).toBeTruthy())
    fireEvent.click(screen.getByText('2026-04-20'))
    expect(onSelect).toHaveBeenCalledWith('2026-04-20')
  })
})

describe('TimelinePanel (horizontal / mobile)', () => {
  it('renders a horizontally scrollable strip and fires onSelect on tap', () => {
    const onSelect = vi.fn()
    render(<TimelinePanel nodes={nodes} selected="2026-05-09" onSelect={onSelect} horizontal />)
    const scroll = screen.getByTestId('timeline-hscroll')
    expect(scroll.style.overflowX).toBe('auto')
    fireEvent.click(screen.getByText('2026-01-17'))
    expect(onSelect).toHaveBeenCalledWith('2026-01-17')
  })
})
