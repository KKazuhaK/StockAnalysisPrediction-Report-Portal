import { describe, it, expect } from 'vitest'
import { API_ENDPOINTS, API_CONVENTIONS } from './apiDoc'

describe('API doc data', () => {
  it('documents every Dify machine endpoint plus the public ones', () => {
    const keys = API_ENDPOINTS.map((e) => `${e.method} ${e.path}`)
    for (const expected of [
      'POST /api/reports',
      'GET /api/reports',
      'GET /api/reports/manifest',
      'GET /api/report',
      'DELETE /api/report',
      'GET /api/runs',
      'GET /api/symbols',
      'GET /api/tracking',
      'PATCH /api/tracking/{id}',
      'GET /healthz',
      'GET /api/version',
    ]) {
      expect(keys).toContain(expected)
    }
  })

  it('every endpoint is fully specified (summary, examples, scope)', () => {
    for (const e of API_ENDPOINTS) {
      expect(e.summary.trim()).not.toBe('')
      expect(e.scope.trim()).not.toBe('')
      expect(e.requestExample).toContain('curl')
      expect(e.responseExample.trim()).not.toBe('')
      // body/query params must declare a valid location and type
      for (const p of e.params) {
        expect(['query', 'body', 'path', 'header']).toContain(p.in)
        expect(p.type.trim()).not.toBe('')
        expect(p.desc.trim()).not.toBe('')
      }
      // error entries use real HTTP status codes
      for (const err of e.errors) {
        expect(err.code).toBeGreaterThanOrEqual(400)
        expect(err.when.trim()).not.toBe('')
      }
    }
  })

  it('has a non-empty conventions preamble', () => {
    expect(API_CONVENTIONS.length).toBeGreaterThan(100)
  })
})
