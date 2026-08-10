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

  // A caller that already fetches on mount (the site chrome, the home feed) would otherwise
  // issue the same request twice within a millisecond of each other.
  it('skipLeading suppresses only the first run, not the interval', async () => {
    vi.useFakeTimers()
    const task = vi.fn(() => Promise.resolve())
    const stop = startVisiblePoll(task, 1000, { skipLeading: true })
    expect(task).toHaveBeenCalledTimes(0)
    await vi.advanceTimersByTimeAsync(1000)
    expect(task).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1000)
    expect(task).toHaveBeenCalledTimes(2)
    stop()
    vi.useRealTimers()
  })
})
