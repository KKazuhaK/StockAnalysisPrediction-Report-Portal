import { describe, it, expect } from 'vitest'
import { specToEndpoints } from './openapiDoc'

const spec = {
  info: { description: 'CONVENTIONS-TEXT' },
  components: {
    schemas: {
      IngestRequest: {
        required: ['symbol'],
        properties: {
          symbol: { type: 'string', description: '代码' },
          tracking: { type: 'array', items: { $ref: '#/components/schemas/TrackingInput' } },
        },
      },
    },
  },
  paths: {
    '/api/v1/reports': {
      post: {
        summary: 'ingest',
        description: 'note-text',
        'x-scope': 'ingest',
        requestBody: { content: { 'application/json': { schema: { $ref: '#/components/schemas/IngestRequest' } } } },
        'x-codeSamples': [{ lang: 'cURL', source: 'curl $BASE/api/v1/reports' }],
        responses: {
          '200': { content: { 'application/json': { example: { ok: true, uid: 'x' } } } },
          '400': { description: 'missing_param' },
        },
      },
      get: {
        summary: 'query',
        'x-scope': 'query',
        parameters: [{ name: 'symbol', in: 'query', schema: { type: 'string' }, description: '代码' }],
        responses: { '200': { content: { 'application/json': { example: { ok: true, items: [] } } } } },
      },
    },
  },
}

describe('specToEndpoints', () => {
  const { conventions, endpoints } = specToEndpoints(spec, 'https://x.com')

  it('takes conventions from info.description', () => {
    expect(conventions).toBe('CONVENTIONS-TEXT')
  })

  it('flattens path × method into endpoints (POST first)', () => {
    const post = endpoints.find((e) => e.method === 'POST' && e.path === '/api/v1/reports')!
    expect(post).toBeTruthy()
    expect(post.scope).toBe('ingest')
    expect(post.summary).toBe('ingest')
    expect(post.notes).toBe('note-text')
  })

  it('resolves body params from a $ref request schema, marking required', () => {
    const post = endpoints.find((e) => e.method === 'POST')!
    const symbol = post.params.find((p) => p.name === 'symbol')!
    expect(symbol.in).toBe('body')
    expect(symbol.required).toBe(true)
    expect(symbol.desc).toBe('代码')
    const tracking = post.params.find((p) => p.name === 'tracking')!
    expect(tracking.type).toContain('array')
    expect(tracking.type).toContain('TrackingInput')
  })

  it('substitutes $BASE in curl, pretty-prints the 200 example, extracts 4xx errors', () => {
    const post = endpoints.find((e) => e.method === 'POST')!
    expect(post.requestExample).toContain('https://x.com')
    expect(post.requestExample).not.toContain('$BASE')
    expect(post.responseExample).toContain('"ok"')
    expect(post.errors.some((e) => e.code === 400)).toBe(true)
  })

  it('reads query parameters for GET', () => {
    const get = endpoints.find((e) => e.method === 'GET')!
    expect(get.params.find((p) => p.name === 'symbol')!.in).toBe('query')
  })
})
