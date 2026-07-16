// Runs a poll only while the page is visible and never overlaps requests. Returning
// a cleanup function makes it fit directly into a React effect without tying the
// scheduling behavior to React itself.
export function startVisiblePoll(task: () => void | Promise<void>, intervalMs: number): () => void {
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

  void run()
  const timer = window.setInterval(run, intervalMs)
  document.addEventListener('visibilitychange', onVisible)
  return () => {
    stopped = true
    window.clearInterval(timer)
    document.removeEventListener('visibilitychange', onVisible)
  }
}
