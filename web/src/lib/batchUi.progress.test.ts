import { describe, expect, it } from 'vitest'
import type { BatchJob } from '../api/types'
import { jobProgressPresentation } from './batchUi'

const job = (overrides: Partial<BatchJob>): BatchJob => ({
  id: 1,
  target_id: 1,
  status: 'running',
  concurrency: 1,
  max_retries: 0,
  total: 4,
  succeeded: 0,
  partial: 0,
  failed: 0,
  created_by: 'alice',
  created_at: '2026-09-09 00:00:00',
  started_at: '',
  finished_at: '',
  ...overrides,
})

describe('jobProgressPresentation', () => {
  it('keeps a running job active when one of its rows has failed', () => {
    expect(jobProgressPresentation(job({ succeeded: 1, failed: 1 }))).toMatchObject({
      done: 2,
      percent: 50,
      status: 'active',
      strokeColor: undefined,
    })
  })

  it('uses warning for a completed job with both successful and failed rows', () => {
    expect(jobProgressPresentation(job({ status: 'finished', succeeded: 3, failed: 1 }))).toMatchObject({
      status: undefined,
      strokeColor: '#faad14',
    })
  })

  it('reserves the exception state for a completed job with no successful rows', () => {
    expect(jobProgressPresentation(job({ status: 'finished', failed: 4 }))).toMatchObject({
      status: 'exception',
      strokeColor: undefined,
    })
  })
})
