import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { List, Typography } from 'antd'
import { clickable } from './clickable'

// Why two antd components are on a11y.test.ts's UNSAFE_ANTD list.
//
// A rule about a third-party library's behaviour rots silently: the library gets fixed, the rule
// stays, and nobody knows whether it is still needed. These pin the behaviour the rule rests on, so
// an antd upgrade that makes either component operable turns a test red and the rule can go.

describe('antd components that are not keyboard-operable on their own', () => {
  it('Typography.Link without href is focusable but Enter does not activate it', async () => {
    const fired = vi.fn()
    render(<Typography.Link onClick={fired}>retry</Typography.Link>)
    const el = screen.getByText('retry')

    expect(el.tagName).toBe('A')
    expect(el.getAttribute('href')).toBeNull()
    expect((el as HTMLElement).tabIndex).toBe(0) // antd does make it focusable

    el.focus()
    await userEvent.keyboard('{Enter}')
    // …and that is the whole problem: an anchor's activation behaviour requires an href, so a
    // keyboard user can focus this and press Enter and nothing at all happens.
    expect(fired).not.toHaveBeenCalled()
  })

  it('List.Item is given tabIndex=-1, so Tab cannot reach it', () => {
    render(<List dataSource={['a']} renderItem={() => <List.Item onClick={vi.fn()}>row</List.Item>} />)
    expect((screen.getByText('row') as HTMLElement).tabIndex).toBe(-1)
  })

  it('clickable() fixes both', async () => {
    const link = vi.fn()
    const row = vi.fn()
    render(
      <>
        <Typography.Link {...clickable(link)}>retry</Typography.Link>
        <List dataSource={['a']} renderItem={() => <List.Item {...clickable(row)}>row</List.Item>} />
      </>,
    )
    const a = screen.getByRole('button', { name: 'retry' })
    a.focus()
    await userEvent.keyboard('{Enter}')
    expect(link).toHaveBeenCalledTimes(1)

    const li = screen.getByRole('button', { name: 'row' })
    expect((li as HTMLElement).tabIndex).toBe(0)
    li.focus()
    await userEvent.keyboard(' ')
    expect(row).toHaveBeenCalledTimes(1)
  })
})
