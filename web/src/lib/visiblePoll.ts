// Runs a poll only while the page is visible and never overlaps requests. Returning
// a cleanup function makes it fit directly into a React effect without tying the
// scheduling behavior to React itself.
//
// skipLeading is for a caller that already loads once on mount: without it the poll's own
// immediate run fires the same request a millisecond later. It suppresses ONLY that first
// run — the interval and the become-visible refresh are unaffected.
export function startVisiblePoll(
  task: () => void | Promise<void>,
  intervalMs: number,
  opts: { skipLeading?: boolean } = {},
): () => void {
  let stopped = false
  let inFlight = false

  const run = async () => {
    if (stopped || inFlight || document.visibilityState !== 'visible') return
    inFlight = true
    try {
      await task()
    } finally {
      inFlight = false
    }
  }
  const onVisible = () => {
    if (document.visibilityState === 'visible') void run()
  }

  if (!opts.skipLeading) void run()
  const timer = window.setInterval(run, intervalMs)
  document.addEventListener('visibilitychange', onVisible)
  return () => {
    stopped = true
    window.clearInterval(timer)
    document.removeEventListener('visibilitychange', onVisible)
  }
}
