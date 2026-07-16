import { describe, expect, it, vi } from 'vitest'
import { startVisiblePoll } from './visiblePoll'

describe('startVisiblePoll', () => {
  it('does not overlap an in-flight poll', async () => {
    vi.useFakeTimers()
    let finish!: () => void
    const task = vi.fn(() => new Promise<void>((resolve) => (finish = resolve)))
    const stop = startVisiblePoll(task, 1000)
    await vi.advanceTimersByTimeAsync(3000)
    expect(task).toHaveBeenCalledTimes(1)
    finish()
    await Promise.resolve()
    await vi.advanceTimersByTimeAsync(1000)
    expect(task).toHaveBeenCalledTimes(2)
    stop()
    vi.useRealTimers()
  })
})
