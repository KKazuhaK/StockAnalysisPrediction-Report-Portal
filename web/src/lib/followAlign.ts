// Keeps a popup aligned to a trigger that MOVES.
//
// antd positions a popover once, when it opens, and then only re-positions on scroll and on window
// resize — the two ways a trigger normally moves. A layout change is a third way it does not watch:
// nothing resizes and nothing scrolls, the page simply re-flows and the trigger slides sideways,
// leaving the popup behind at coordinates that no longer mean anything.
//
// There is no event for "the page re-flowed", so this watches the trigger's own box instead, and it
// watches per animation frame because the re-flow is usually a CSS transition: a single re-align
// would snap the popup to a position the trigger is still travelling away from. It lets go as soon
// as the trigger holds still, so nothing keeps polling for as long as a popup is open.

export interface FollowOptions {
  /** Consecutive unchanged frames that count as "settled". ~5 frames is 80ms of stillness. */
  stillFrames?: number
  /** Hard cap, so a permanently animating layout cannot spin a frame callback forever. */
  maxFrames?: number
}

/**
 * Watches `el` and calls `realign` on every frame its position or size changes. Returns a cleanup
 * function; it is also self-limiting, so a caller that forgets to clean up leaks nothing.
 */
export function followTrigger(el: Element, realign: () => void, opts: FollowOptions = {}): () => void {
  const stillFrames = opts.stillFrames ?? 5
  const maxFrames = opts.maxFrames ?? 60
  // Guard the non-browser runtimes (SSR, a bare unit test) rather than making every caller ask.
  if (typeof requestAnimationFrame !== 'function') return () => {}

  let handle = 0
  let seen = 0
  let still = 0
  let last = box(el)

  const tick = () => {
    seen += 1
    const now = box(el)
    if (now !== last) {
      last = now
      still = 0
      realign()
    } else if ((still += 1) >= stillFrames) {
      return
    }
    if (seen >= maxFrames) return
    handle = requestAnimationFrame(tick)
  }

  handle = requestAnimationFrame(tick)
  return () => cancelAnimationFrame(handle)
}

// The trigger's geometry as a comparable string. Raw values, not rounded: getBoundingClientRect is
// deterministic for a static layout, so "unchanged" really does mean the re-flow has finished, and
// rounding would stop the follow up to a pixel early in the slow tail of an ease-out.
function box(el: Element): string {
  const r = el.getBoundingClientRect()
  return `${r.left},${r.top},${r.width},${r.height}`
}
