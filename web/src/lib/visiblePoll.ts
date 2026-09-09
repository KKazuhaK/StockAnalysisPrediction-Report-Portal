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

// Runs one task when its deadline is due and the page is visible. A hidden page remembers that the
// deadline passed and catches up once on visibility; it never relies on a throttled hidden timer to
// keep market data current. The caller schedules the next deadline from the response that arrives.
export function startVisibleDeadline(task: () => void | Promise<void>, delayMs: number): () => void {
  let stopped = false
  let fired = false
  const dueAt = Date.now() + Math.max(0, delayMs)

  const run = () => {
    if (stopped || fired || document.visibilityState !== 'visible' || Date.now() < dueAt) return
    fired = true
    void task()
  }
  const onVisible = () => run()
  const timer = window.setTimeout(run, Math.max(0, delayMs))
  document.addEventListener('visibilitychange', onVisible)
  return () => {
    stopped = true
    window.clearTimeout(timer)
    document.removeEventListener('visibilitychange', onVisible)
  }
}
