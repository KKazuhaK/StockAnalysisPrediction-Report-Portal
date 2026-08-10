// Who is currently watching the queue.
//
// The header badge polls the queue summary every 12 seconds so a number can sit next to the queue
// button. The queue page and the queue drawer poll the same endpoint every 3 seconds, because they
// show the thing itself. When one of those is open the badge is asking for what is already on
// screen, four times slower and always more stale.
//
// So a queue view announces itself here for as long as it is mounted, and the badge stands down.
// Leaving the view resumes it, and the poll is visibility-gated anyway, so nothing has to be
// re-synchronised — the next tick after it resumes is the truth.

let watchers = 0

/** Register a queue view for as long as it is mounted. Returns the deregister function. */
export function watchQueue(): () => void {
  watchers += 1
  let released = false
  return () => {
    if (released) return
    released = true
    watchers -= 1
  }
}

/** Whether something on screen is already showing the queue in more detail than a badge can. */
export function queueOnScreen(): boolean {
  return watchers > 0
}
