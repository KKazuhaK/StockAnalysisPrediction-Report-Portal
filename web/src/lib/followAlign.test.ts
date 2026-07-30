import { afterEach, describe, expect, it, vi } from 'vitest'
import { followTrigger } from './followAlign'

// An element whose left edge walks through a scripted list of positions, one read per call, so a
// test can describe a layout animation without a browser.
function walking(positions: number[]): Element {
  let i = 0
  return {
    getBoundingClientRect: () => {
      const left = positions[Math.min(i, positions.length - 1)]
      i += 1
      return { left, top: 0, width: 100, height: 20 } as DOMRect
    },
  } as unknown as Element
}

function frames(n: number) {
  vi.advanceTimersByTime(n * 17)
}

describe('followTrigger', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('realigns while the trigger moves, then stops on its own once it settles', () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] })
    // Read 0 is taken at setup; the moves are what the frames see.
    const el = walking([0, 40, 80, 100, 100])
    const realign = vi.fn()
    followTrigger(el, realign)

    frames(3)
    expect(realign).toHaveBeenCalledTimes(3)

    // It must let go rather than burn a frame callback for as long as the popover is open.
    frames(60)
    expect(realign).toHaveBeenCalledTimes(3)
  })

  it('gives up on a layout that never settles', () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] })
    let x = 0
    const el = { getBoundingClientRect: () => ({ left: (x += 10), top: 0 }) as DOMRect } as unknown as Element
    const realign = vi.fn()
    followTrigger(el, realign, { maxFrames: 8 })

    frames(200)
    expect(realign).toHaveBeenCalledTimes(8)
  })

  it('stops when the caller cleans up mid-animation', () => {
    vi.useFakeTimers({ toFake: ['requestAnimationFrame', 'cancelAnimationFrame'] })
    const el = walking([0, 40, 80, 120, 160])
    const realign = vi.fn()
    const stop = followTrigger(el, realign)

    frames(1)
    expect(realign).toHaveBeenCalledTimes(1)
    stop()
    frames(20)
    expect(realign).toHaveBeenCalledTimes(1)
  })

  it('is inert where there are no animation frames to hook', () => {
    const raf = globalThis.requestAnimationFrame
    // @ts-expect-error deliberately removing it to stand in for a non-browser runtime
    globalThis.requestAnimationFrame = undefined
    try {
      const realign = vi.fn()
      expect(() => followTrigger(walking([0, 40]), realign)()).not.toThrow()
      expect(realign).not.toHaveBeenCalled()
    } finally {
      globalThis.requestAnimationFrame = raf
    }
  })
})
